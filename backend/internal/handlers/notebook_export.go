package handlers

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

//go:embed assets/logo.png
var logoPNG []byte

var logoBase64 = base64.StdEncoding.EncodeToString(logoPNG)

// ExportNotebookHTML exports a notebook as a standalone HTML file.
// GET /api/v1/notebooks/:id/export/html
func (h *NotebookHandler) ExportNotebookHTML(c *gin.Context) {
	notebookID := c.Param("id")
	if !checkNotebookReadAccess(c, notebookID) {
		return
	}
	db := database.GetDB()

	// Load notebook
	var name, language string
	if err := db.QueryRow("SELECT name, language FROM notebooks WHERE id = $1", notebookID).Scan(&name, &language); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "notebook not found"})
		return
	}

	// Load cells
	rows, err := db.Query(
		`SELECT type, source, cell_order, last_output FROM notebook_cells
		 WHERE notebook_id = $1 ORDER BY cell_order`, notebookID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load cells"})
		return
	}
	defer rows.Close()

	var cells []exportCell
	for rows.Next() {
		var cl exportCell
		var output []byte
		if err := rows.Scan(&cl.Type, &cl.Source, &cl.Order, &output); err != nil {
			continue
		}
		cl.LastOutput = output
		cells = append(cells, cl)
	}

	htmlContent := generateNotebookHTML(name, language, cells)

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.html"`, name))
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(htmlContent))
}

// ImportNotebook imports a notebook from JSON, IPYNB, or HTML file.
// POST /api/v1/notebooks/import
func (h *NotebookHandler) ImportNotebook(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file provided"})
		return
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read file"})
		return
	}

	filename := strings.ToLower(header.Filename)

	if strings.HasSuffix(filename, ".json") || strings.HasSuffix(filename, ".ipynb") {
		h.importFromJSON(c, content)
	} else if strings.HasSuffix(filename, ".html") {
		h.importFromHTML(c, string(content), header.Filename)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported file format. Use .json, .ipynb or .html"})
	}
}

// --- Import from JSON/IPYNB ---

func (h *NotebookHandler) importFromJSON(c *gin.Context, content []byte) {
	var data map[string]interface{}
	if err := json.Unmarshal(content, &data); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON/IPYNB format"})
		return
	}

	// Extract name
	name := stringVal(data, "name")
	if name == "" {
		if md, ok := data["metadata"].(map[string]interface{}); ok {
			name = stringVal(md, "name")
		}
	}
	if name == "" {
		name = "Imported Notebook"
	}

	// Extract language
	language := stringVal(data, "language")
	if language == "" {
		if md, ok := data["metadata"].(map[string]interface{}); ok {
			if li, ok := md["language_info"].(map[string]interface{}); ok {
				language = stringVal(li, "name")
			}
			if language == "" {
				if ks, ok := md["kernelspec"].(map[string]interface{}); ok {
					language = stringVal(ks, "language")
				}
			}
		}
	}
	language = normalizeLanguage(language)

	// Parse cells
	var cells []importCellData

	if rawCells, ok := data["cells"].([]interface{}); ok {
		for _, raw := range rawCells {
			cellMap, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}

			// Source: string or []string (Jupyter format)
			source := stringVal(cellMap, "source")
			if source == "" {
				if sourceList, ok := cellMap["source"].([]interface{}); ok {
					var sb strings.Builder
					for _, line := range sourceList {
						sb.WriteString(fmt.Sprintf("%v", line))
					}
					source = sb.String()
				}
			}

			// Type
			cellType := stringVal(cellMap, "cell_type")
			if cellType == "" {
				cellType = stringVal(cellMap, "type")
			}
			if cellType == "" {
				cellType = "code"
			}

			// Outputs
			var lastOutput json.RawMessage
			if outputs, ok := cellMap["outputs"].([]interface{}); ok && len(outputs) > 0 {
				wrapped := map[string]interface{}{"outputs": outputs}
				lastOutput, _ = json.Marshal(wrapped)
			}
			if lastOutput == nil {
				if lo, ok := cellMap["last_output"]; ok && lo != nil {
					lastOutput, _ = json.Marshal(lo)
				}
			}

			cells = append(cells, importCellData{
				Type:       strings.ToLower(cellType),
				Source:     source,
				LastOutput: lastOutput,
			})
		}
	}

	// Save to DB
	nbID, err := saveImportedNotebook(c, name, language, cells)
	if err != nil {
		log.Error().Err(err).Msg("failed to save imported notebook")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save notebook"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nbID, "name": name})
}

// --- Import from HTML ---

var (
	codeRE     = regexp.MustCompile(`(?s)<pre class="code"><code[^>]*>(.*?)</code></pre>`)
	outputRE   = regexp.MustCompile(`(?s)<pre class="output([^"]*)">(.*?)</pre>`)
	htmlOutRE  = regexp.MustCompile(`(?s)<div class="output html">(.*?)</div>`)
	titleRE    = regexp.MustCompile(`<title>([^<]+)</title>`)
	languageRE = regexp.MustCompile(`Language:\s*(\w+)`)
	markdownRE = regexp.MustCompile(`data-markdown="((?:[^"\\]|\\.)*)"`)
)

func (h *NotebookHandler) importFromHTML(c *gin.Context, htmlContent string, filename string) {
	// Extract name
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	if match := titleRE.FindStringSubmatch(htmlContent); len(match) > 1 {
		name = strings.ReplaceAll(match[1], " - SparkLabX", "")
	}

	// Extract language
	language := "python"
	if match := languageRE.FindStringSubmatch(htmlContent); len(match) > 1 {
		language = normalizeLanguage(match[1])
	}

	var cells []importCellData

	// Split on cell divs, preserving type (markdown-cell vs code-cell)
	parts := strings.Split(htmlContent, `<div class="cell `)
	for _, part := range parts[1:] {
		// Determine cell type from class
		if strings.HasPrefix(part, "markdown-cell") {
			// Extract raw markdown from data-markdown attribute
			mdMatch := markdownRE.FindStringSubmatch(part)
			if len(mdMatch) < 2 {
				continue
			}
			source := html.UnescapeString(mdMatch[1])
			cells = append(cells, importCellData{
				Type:   "markdown",
				Source: source,
			})
			continue
		}

		// Code cell — extract source
		codeMatch := codeRE.FindStringSubmatch(part)
		if len(codeMatch) < 2 {
			continue
		}
		source := html.UnescapeString(codeMatch[1])

		// Extract outputs
		var outputs []map[string]interface{}

		for _, outMatch := range outputRE.FindAllStringSubmatch(part, -1) {
			if len(outMatch) < 3 {
				continue
			}
			classes := outMatch[1]
			text := html.UnescapeString(outMatch[2])

			outType := "stream"
			if strings.Contains(classes, "error") {
				outType = "error"
			}

			outObj := map[string]interface{}{
				"type": outType,
				"name": "stdout",
				"text": text,
				"data": map[string]interface{}{"text/plain": text},
			}
			if outType == "error" {
				outObj["name"] = "stderr"
				outObj["traceback"] = strings.Split(text, "\n")
				outObj["ename"] = "Error"
				outObj["evalue"] = ""
			}
			outputs = append(outputs, outObj)
		}

		for _, htmlMatch := range htmlOutRE.FindAllStringSubmatch(part, -1) {
			if len(htmlMatch) < 2 {
				continue
			}
			outputs = append(outputs, map[string]interface{}{
				"type": "execute_result",
				"data": map[string]interface{}{"text/html": htmlMatch[1]},
			})
		}

		var lastOutput json.RawMessage
		if len(outputs) > 0 {
			wrapped := map[string]interface{}{"outputs": outputs}
			lastOutput, _ = json.Marshal(wrapped)
		}

		cells = append(cells, importCellData{
			Type:       "code",
			Source:     source,
			LastOutput: lastOutput,
		})
	}

	if len(cells) == 0 {
		cells = append(cells, importCellData{Type: "code", Source: ""})
	}

	nbID, err := saveImportedNotebook(c, name, language, cells)
	if err != nil {
		log.Error().Err(err).Msg("failed to save imported notebook")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save notebook"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nbID, "name": name})
}

// --- Save imported notebook to DB ---

// importCellData is the common cell structure for import.
type importCellData struct {
	Type       string
	Source     string
	LastOutput json.RawMessage
}

func saveImportedNotebook(c *gin.Context, name, language string, cells []importCellData) (string, error) {
	db := database.GetDB()
	nbID := uuid.New().String()
	now := time.Now()

	ownerID, _ := c.Get("admin_id")
	if ownerID == nil {
		ownerID, _ = c.Get("user_id")
	}

	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`INSERT INTO notebooks (id, name, language, owner_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		nbID, name, language, ownerID, now, now)
	if err != nil {
		return "", fmt.Errorf("failed to insert notebook: %w", err)
	}

	for i, cl := range cells {
		cellID := uuid.New().String()
		_, err = tx.Exec(
			`INSERT INTO notebook_cells (id, notebook_id, type, source, cell_order, last_output, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			cellID, nbID, cl.Type, cl.Source, i, cl.LastOutput, now, now)
		if err != nil {
			return "", fmt.Errorf("failed to insert cell: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}

	return nbID, nil
}

// --- HTML Generation ---

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// exportCell represents a cell for HTML export.
type exportCell struct {
	Type       string
	Source     string
	Order      int
	LastOutput json.RawMessage
}

func generateNotebookHTML(name, language string, cells []exportCell) string {
	var cellsHTML strings.Builder
	codeIndex := 0

	for _, cell := range cells {
		if strings.HasPrefix(cell.Source, "// __SPARK_INIT__") || strings.HasPrefix(cell.Source, "# __SPARK_INIT__") {
			continue
		}

		cellType := strings.ToLower(cell.Type)

		if cellType == "markdown" {
			// Store raw markdown in a data attribute; rendered client-side via marked.js
			cellsHTML.WriteString(fmt.Sprintf(`<div class="cell markdown-cell"><div class="markdown-content" data-markdown="%s"></div></div>`, html.EscapeString(cell.Source)))
			continue
		}

		codeIndex++

		// Code cell
		var outputHTML strings.Builder
		if len(cell.LastOutput) > 0 {
			var lo struct {
				Outputs []map[string]interface{} `json:"outputs"`
			}
			if json.Unmarshal(cell.LastOutput, &lo) == nil {
				for _, out := range lo.Outputs {
					outType, _ := out["type"].(string)

					// Error outputs
					if outType == "error" {
						traceback, _ := out["traceback"].([]interface{})
						if len(traceback) > 0 {
							var lines []string
							for _, t := range traceback {
								lines = append(lines, fmt.Sprintf("%v", t))
							}
							errorText := strings.Join(lines, "\n")
							outputHTML.WriteString(fmt.Sprintf(`<pre class="output error">%s</pre>`, html.EscapeString(stripANSI(errorText))))
						} else {
							ename, _ := out["ename"].(string)
							evalue, _ := out["evalue"].(string)
							outputHTML.WriteString(fmt.Sprintf(`<pre class="output error">%s: %s</pre>`, html.EscapeString(ename), html.EscapeString(evalue)))
						}
						continue
					}

					// Data outputs (prioritize HTML)
					if data, ok := out["data"].(map[string]interface{}); ok {
						if htmlData, ok := data["text/html"].(string); ok {
							outputHTML.WriteString(fmt.Sprintf(`<div class="output html">%s</div>`, htmlData))
							continue
						}
						if plain, ok := data["text/plain"].(string); ok {
							outputHTML.WriteString(fmt.Sprintf(`<pre class="output">%s</pre>`, html.EscapeString(stripANSI(plain))))
							continue
						}
					}

					// Stream text
					if text, ok := out["text"].(string); ok {
						outputHTML.WriteString(fmt.Sprintf(`<pre class="output">%s</pre>`, html.EscapeString(stripANSI(text))))
					}
				}
			}
		}

		cellsHTML.WriteString(fmt.Sprintf(
			`<div class="cell code-cell"><div class="cell-label">In [%d]:</div><pre class="code"><code class="language-%s">%s</code></pre>%s</div>`,
			codeIndex,
			html.EscapeString(strings.ToLower(language)),
			html.EscapeString(cell.Source),
			outputHTML.String(),
		))
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s - SparkLabX</title>
    <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.9.0/styles/github.min.css">
    <script src="https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.9.0/highlight.min.js"></script>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.9.0/languages/scala.min.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/marked/marked.min.js"></script>
    <script>
        document.addEventListener('DOMContentLoaded', function() {
            hljs.highlightAll();
            document.querySelectorAll('.markdown-content[data-markdown]').forEach(function(el) {
                el.innerHTML = marked.parse(el.getAttribute('data-markdown'));
            });
        });
    </script>
    <style>
        * { box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 900px; margin: 0 auto; padding: 40px 20px; background: #f8f9fa; color: #24292e; line-height: 1.6; }
        h1 { margin-bottom: 10px; }
        .metadata { color: #6a737d; margin-bottom: 30px; font-size: 14px; }
        .cell { margin-bottom: 20px; background: white; border: 1px solid #e1e4e8; border-radius: 6px; overflow: hidden; }
        .cell-label { background: #f6f8fa; padding: 8px 16px; font-size: 12px; color: #6a737d; border-bottom: 1px solid #e1e4e8; }
        pre.code { margin: 0; padding: 16px; background: #fafbfc; overflow-x: auto; font-family: 'SFMono-Regular', Consolas, monospace; font-size: 13px; }
        pre.output { margin: 0; padding: 16px; background: #f6f8fa; border-top: 1px solid #e1e4e8; overflow-x: auto; font-family: 'SFMono-Regular', Consolas, monospace; font-size: 13px; white-space: pre-wrap; }
        pre.output.error { color: #d73a49; background: #ffeef0; }
        .output.html { padding: 16px; border-top: 1px solid #e1e4e8; }
        .output.html table { border-collapse: collapse; width: 100%%; }
        .output.html th, .output.html td { border: 1px solid #e1e4e8; padding: 8px 12px; text-align: left; }
        .output.html th { background: #f6f8fa; }
        .markdown-cell { background: white; }
        .markdown-content { padding: 16px; }
        .markdown-content h1, .markdown-content h2, .markdown-content h3,
        .markdown-content h4, .markdown-content h5, .markdown-content h6 { margin-top: 24px; margin-bottom: 12px; font-weight: 600; line-height: 1.25; }
        .markdown-content h1 { font-size: 2em; border-bottom: 1px solid #e1e4e8; padding-bottom: 8px; }
        .markdown-content h2 { font-size: 1.5em; border-bottom: 1px solid #e1e4e8; padding-bottom: 6px; }
        .markdown-content h3 { font-size: 1.25em; }
        .markdown-content p { margin-top: 0; margin-bottom: 16px; }
        .markdown-content ul, .markdown-content ol { margin-bottom: 16px; padding-left: 2em; }
        .markdown-content li { margin-bottom: 4px; }
        .markdown-content table { border-collapse: collapse; width: 100%%; margin-bottom: 16px; }
        .markdown-content th, .markdown-content td { border: 1px solid #e1e4e8; padding: 8px 12px; text-align: left; }
        .markdown-content th { background: #f6f8fa; font-weight: 600; }
        .markdown-content tr:nth-child(even) { background: #f8f9fa; }
        .markdown-content code { background: #f3f4f6; padding: 2px 5px; border-radius: 3px; font-size: 85%%; font-family: 'SFMono-Regular', Consolas, monospace; }
        .markdown-content pre { background: #f6f8fa; padding: 16px; border-radius: 6px; overflow-x: auto; margin-bottom: 16px; }
        .markdown-content pre code { background: none; padding: 0; font-size: 13px; }
        .markdown-content blockquote { border-left: 4px solid #dfe2e5; padding: 0 16px; color: #6a737d; margin: 0 0 16px 0; }
        .markdown-content hr { border: none; border-top: 1px solid #e1e4e8; margin: 24px 0; }
        .markdown-content a { color: #0366d6; text-decoration: none; }
        .markdown-content a:hover { text-decoration: underline; }
        .footer { margin-top: 40px; text-align: center; color: #6a737d; font-size: 12px; }
        .header { display: flex; align-items: center; gap: 12px; margin-bottom: 20px; }
        .logo { height: 96px; width: auto; }
        .header-text h1 { margin: 0; font-size: 24px; }
        .header-text .brand { color: #6a737d; font-size: 12px; margin: 0; }
    </style>
</head>
<body>
    <div class="header">
        <img class="logo" src="data:image/png;base64,%s" alt="SparkLabX Logo" />
        <div class="header-text">
            <h1>%s</h1>
            <p class="brand">SparkLabX Notebook</p>
        </div>
    </div>
    <div class="metadata">
        Language: %s | Exported on %s UTC
    </div>
    %s
    <div class="footer">Generated by SparkLabX</div>
</body>
</html>`,
		html.EscapeString(name),
		logoBase64,
		html.EscapeString(name),
		html.EscapeString(language),
		time.Now().UTC().Format("2006-01-02 15:04:05"),
		cellsHTML.String(),
	)
}

// --- Helpers ---

func stringVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func normalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if strings.Contains(lang, "scala") || strings.Contains(lang, "almond") {
		return "scala"
	}
	if strings.Contains(lang, "python") || lang == "" {
		return "python"
	}
	return lang
}
