package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

type NotebookHandler struct{}

func NewNotebookHandler() *NotebookHandler {
	return &NotebookHandler{}
}

// getOwner returns owner_id and owner_type from gin context (admin or student).
func getOwner(c *gin.Context) (string, string) {
	if id := c.GetString("admin_id"); id != "" {
		return id, "admin"
	}
	if id := c.GetString("user_id"); id != "" {
		return id, "candidate"
	}
	return "", ""
}

// checkNotebookReadAccess verifies the caller can read the notebook.
// Admins can read all notebooks. Students can read their own notebooks and public notebooks.
func checkNotebookReadAccess(c *gin.Context, notebookID string) bool {
	role, _ := c.Get("role")
	if role != "student" {
		return true
	}
	ownerID, _ := getOwner(c)
	db := database.GetDB()
	var nbOwnerID string
	var isPublic bool
	if err := db.QueryRow("SELECT owner_id, is_public FROM notebooks WHERE id = $1", notebookID).Scan(&nbOwnerID, &isPublic); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "notebook not found"})
		return false
	}
	if nbOwnerID != ownerID && !isPublic {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return false
	}
	return true
}

// checkNotebookWriteAccess verifies the caller can modify or execute against the notebook.
// Admins can write all notebooks. Students can write only their own notebooks.
func checkNotebookWriteAccess(c *gin.Context, notebookID string) bool {
	role, _ := c.Get("role")
	if role != "student" {
		return true
	}
	ownerID, _ := getOwner(c)
	db := database.GetDB()
	var nbOwnerID string
	if err := db.QueryRow("SELECT owner_id FROM notebooks WHERE id = $1", notebookID).Scan(&nbOwnerID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "notebook not found"})
		return false
	}
	if nbOwnerID != ownerID {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return false
	}
	return true
}

// ListNotebooks returns notebooks for the authenticated admin.
func (h *NotebookHandler) ListNotebooks(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	ownerID, ownerType := getOwner(c)
	db := database.GetDB()

	var total int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM notebooks WHERE (owner_id = $1 AND owner_type = $2) OR is_public = true",
		ownerID, ownerType,
	).Scan(&total); err != nil {
		log.Error().Err(err).Msg("failed to count notebooks")
	}

	offset := (page - 1) * pageSize
	rows, err := db.Query(
		`SELECT n.id, n.name, n.description, n.language, n.owner_id, n.owner_type,
		        n.is_public, n.created_at, n.updated_at,
		        (SELECT COUNT(*) FROM notebook_cells WHERE notebook_id = n.id) as cell_count
		 FROM notebooks n
		 WHERE (n.owner_id = $1 AND n.owner_type = $2) OR n.is_public = true
		 ORDER BY n.updated_at DESC
		 LIMIT $3 OFFSET $4`,
		ownerID, ownerType, pageSize, offset,
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to list notebooks")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list notebooks"})
		return
	}
	defer rows.Close()

	type NotebookItem struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		Description string    `json:"description"`
		Language    string    `json:"language"`
		OwnerID     string    `json:"owner_id"`
		OwnerType   string    `json:"owner_type"`
		IsPublic    bool      `json:"is_public"`
		CreatedAt   time.Time `json:"created_at"`
		UpdatedAt   time.Time `json:"updated_at"`
		CellCount   int       `json:"cell_count"`
	}

	items := []NotebookItem{}
	for rows.Next() {
		var n NotebookItem
		if err := rows.Scan(&n.ID, &n.Name, &n.Description, &n.Language, &n.OwnerID, &n.OwnerType,
			&n.IsPublic, &n.CreatedAt, &n.UpdatedAt, &n.CellCount); err != nil {
			continue
		}
		items = append(items, n)
	}

	c.JSON(http.StatusOK, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// CreateNotebook creates a new notebook.
func (h *NotebookHandler) CreateNotebook(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
		Language    string `json:"language"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
		return
	}

	lang := strings.ToLower(req.Language)
	if lang == "" {
		lang = "python"
	}

	ownerID, ownerType := getOwner(c)
	db := database.GetDB()

	id := uuid.New()
	now := time.Now()

	_, err := db.Exec(
		`INSERT INTO notebooks (id, name, description, language, owner_id, owner_type, is_public, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, false, $7, $8)`,
		id, req.Name, req.Description, lang, ownerID, ownerType, now, now,
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to create notebook")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create notebook"})
		return
	}

	log.Info().Str("notebook_id", id.String()).Str("name", req.Name).Msg("notebook created")
	c.JSON(http.StatusCreated, gin.H{
		"id":          id,
		"name":        req.Name,
		"description": req.Description,
		"language":    lang,
		"owner_id":    ownerID,
		"owner_type":  ownerType,
		"is_public":   false,
		"cells":       []interface{}{},
		"created_at":  now,
		"updated_at":  now,
	})
}

// GetNotebook returns a notebook with its cells.
func (h *NotebookHandler) GetNotebook(c *gin.Context) {
	id := c.Param("id")
	db := database.GetDB()

	var nb struct {
		ID            string    `json:"id"`
		Name          string    `json:"name"`
		Description   string    `json:"description"`
		Language      string    `json:"language"`
		OwnerID       string    `json:"owner_id"`
		OwnerType     string    `json:"owner_type"`
		IsPublic      bool      `json:"is_public"`
		CreatedAt     time.Time `json:"created_at"`
		UpdatedAt     time.Time `json:"updated_at"`
		ClusterConfig []byte
	}
	err := db.QueryRow(
		`SELECT id, name, description, language, owner_id, owner_type, is_public, created_at, updated_at, COALESCE(cluster_config, '{}'::jsonb)
		 FROM notebooks WHERE id = $1`, id,
	).Scan(&nb.ID, &nb.Name, &nb.Description, &nb.Language, &nb.OwnerID, &nb.OwnerType,
		&nb.IsPublic, &nb.CreatedAt, &nb.UpdatedAt, &nb.ClusterConfig)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "notebook not found"})
		return
	}

	// Inline access check — we already fetched owner_id/is_public above, so skip
	// the redundant query that checkNotebookReadAccess() would make.
	if role, _ := c.Get("role"); role == "student" {
		callerID, _ := getOwner(c)
		if nb.OwnerID != callerID && !nb.IsPublic {
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
			return
		}
	}

	// Fetch cells
	rows, err := db.Query(
		`SELECT id, notebook_id, type, source, cell_order, last_output, last_execution_time_ms, execution_count, created_at, updated_at
		 FROM notebook_cells WHERE notebook_id = $1 ORDER BY cell_order ASC`, id,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch cells"})
		return
	}
	defer rows.Close()

	type CellItem struct {
		ID                  string          `json:"id"`
		NotebookID          string          `json:"notebook_id"`
		Type                string          `json:"type"`
		Source              string          `json:"source"`
		Order               int             `json:"order"`
		LastOutput          json.RawMessage `json:"last_output,omitempty"`
		LastExecutionTimeMs *int            `json:"last_execution_time_ms,omitempty"`
		ExecutionCount      *int            `json:"execution_count,omitempty"`
		CreatedAt           time.Time       `json:"created_at"`
		UpdatedAt           time.Time       `json:"updated_at"`
	}

	cells := []CellItem{}
	for rows.Next() {
		var cell CellItem
		var output []byte
		if err := rows.Scan(&cell.ID, &cell.NotebookID, &cell.Type, &cell.Source, &cell.Order,
			&output, &cell.LastExecutionTimeMs, &cell.ExecutionCount, &cell.CreatedAt, &cell.UpdatedAt); err != nil {
			log.Error().Err(err).Msg("failed to scan cell row")
			continue
		}
		cell.Type = strings.ToLower(cell.Type)
		if output != nil {
			cell.LastOutput = json.RawMessage(output)
		}
		cells = append(cells, cell)
	}

	c.JSON(http.StatusOK, gin.H{
		"id":              nb.ID,
		"name":            nb.Name,
		"description":     nb.Description,
		"language":        strings.ToLower(nb.Language),
		"owner_id":        nb.OwnerID,
		"owner_type":      nb.OwnerType,
		"user_id":         nb.OwnerID,
		"is_public":       nb.IsPublic,
		"cluster_config":  json.RawMessage(nb.ClusterConfig),
		"tags":            []string{},
		"created_at":      nb.CreatedAt,
		"updated_at":      nb.UpdatedAt,
		"cells":           cells,
	})
}

// UpdateNotebook updates notebook metadata.
func (h *NotebookHandler) UpdateNotebook(c *gin.Context) {
	id := c.Param("id")
	if !checkNotebookWriteAccess(c, id) {
		return
	}
	var req struct {
		Name          *string                `json:"name"`
		Description   *string                `json:"description"`
		Language      *string                `json:"language"`
		ClusterConfig map[string]interface{} `json:"cluster_config,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	db := database.GetDB()
	updates := []string{}
	args := []interface{}{}
	idx := 1

	if req.Name != nil {
		updates = append(updates, "name = $"+strconv.Itoa(idx))
		args = append(args, *req.Name)
		idx++
	}
	if req.Description != nil {
		updates = append(updates, "description = $"+strconv.Itoa(idx))
		args = append(args, *req.Description)
		idx++
	}
	if req.Language != nil {
		updates = append(updates, "language = $"+strconv.Itoa(idx))
		args = append(args, strings.ToLower(*req.Language))
		idx++
	}
	if req.ClusterConfig != nil {
		configJSON, _ := json.Marshal(req.ClusterConfig)
		updates = append(updates, "cluster_config = $"+strconv.Itoa(idx))
		args = append(args, string(configJSON))
		idx++
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	updates = append(updates, "updated_at = $"+strconv.Itoa(idx))
	args = append(args, time.Now())
	idx++
	args = append(args, id)

	query := "UPDATE notebooks SET " + strings.Join(updates, ", ") + " WHERE id = $" + strconv.Itoa(idx)
	_, err := db.Exec(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update notebook"})
		return
	}

	h.GetNotebook(c)
}

// DeleteNotebook deletes a notebook and its cells (cascade).
func (h *NotebookHandler) DeleteNotebook(c *gin.Context) {
	id := c.Param("id")
	if !checkNotebookWriteAccess(c, id) {
		return
	}
	db := database.GetDB()

	result, err := db.Exec("DELETE FROM notebooks WHERE id = $1", id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete notebook"})
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "notebook not found"})
		return
	}

	log.Info().Str("notebook_id", id).Msg("notebook deleted")
	c.JSON(http.StatusOK, gin.H{"message": "notebook deleted"})
}

// --- Cell operations ---

// CreateCell adds a cell to a notebook.
func (h *NotebookHandler) CreateCell(c *gin.Context) {
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	var req struct {
		Type       string `json:"type" binding:"required"`
		Source     string `json:"source"`
		AfterOrder *int   `json:"after_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
		return
	}

	// Accept after_order from query param or body (xdata compat)
	if req.AfterOrder == nil {
		if v := c.Query("after_order"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				req.AfterOrder = &n
			}
		}
	}

	db := database.GetDB()
	id := uuid.New()
	now := time.Now()

	var maxOrder int
	if err := db.QueryRow("SELECT COALESCE(MAX(cell_order), 0) FROM notebook_cells WHERE notebook_id = $1", notebookID).Scan(&maxOrder); err != nil {
		log.Error().Err(err).Str("notebook_id", notebookID).Msg("failed to get max cell order")
	}

	order := maxOrder + 1
	if req.AfterOrder != nil {
		order = *req.AfterOrder + 1
		if _, err := db.Exec("UPDATE notebook_cells SET cell_order = cell_order + 1 WHERE notebook_id = $1 AND cell_order >= $2",
			notebookID, order); err != nil {
			log.Error().Err(err).Msg("failed to shift cell orders")
		}
	}

	_, err := db.Exec(
		`INSERT INTO notebook_cells (id, notebook_id, type, source, cell_order, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, notebookID, req.Type, req.Source, order, now, now,
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to create cell")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create cell"})
		return
	}

	if _, err := db.Exec("UPDATE notebooks SET updated_at = $1 WHERE id = $2", now, notebookID); err != nil {
		log.Error().Err(err).Msg("failed to update notebook timestamp")
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":          id,
		"notebook_id": notebookID,
		"type":        strings.ToLower(req.Type),
		"source":      req.Source,
		"order":       order,
		"created_at":  now,
		"updated_at":  now,
	})
}

// UpdateCell updates a cell's content or output.
func (h *NotebookHandler) UpdateCell(c *gin.Context) {
	cellID := c.Param("cellId")
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	if cellID == "" || cellID == "undefined" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cell ID"})
		return
	}
	var req struct {
		Source              *string          `json:"source"`
		Type                *string          `json:"type"`
		Order               *int             `json:"order"`
		LastOutput          *json.RawMessage `json:"last_output"`
		LastExecutionTimeMs *int             `json:"last_execution_time_ms"`
		ExecutionCount      *int             `json:"execution_count"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	db := database.GetDB()
	updates := []string{}
	args := []interface{}{}
	idx := 1

	if req.Source != nil {
		updates = append(updates, "source = $"+strconv.Itoa(idx))
		args = append(args, *req.Source)
		idx++
	}
	if req.Type != nil {
		updates = append(updates, "type = $"+strconv.Itoa(idx))
		args = append(args, *req.Type)
		idx++
	}
	if req.Order != nil {
		updates = append(updates, "cell_order = $"+strconv.Itoa(idx))
		args = append(args, *req.Order)
		idx++
	}
	if req.LastOutput != nil {
		updates = append(updates, "last_output = $"+strconv.Itoa(idx))
		args = append(args, []byte(*req.LastOutput))
		idx++
	}
	if req.LastExecutionTimeMs != nil {
		updates = append(updates, "last_execution_time_ms = $"+strconv.Itoa(idx))
		args = append(args, *req.LastExecutionTimeMs)
		idx++
	}
	if req.ExecutionCount != nil {
		updates = append(updates, "execution_count = $"+strconv.Itoa(idx))
		args = append(args, *req.ExecutionCount)
		idx++
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	updates = append(updates, "updated_at = $"+strconv.Itoa(idx))
	args = append(args, time.Now())
	idx++
	args = append(args, cellID)
	idx++
	args = append(args, notebookID)

	// WHERE includes both cell ID and notebook ID to prevent cross-notebook cell modification
	query := "UPDATE notebook_cells SET " + strings.Join(updates, ", ") + " WHERE id = $" + strconv.Itoa(idx-1) + " AND notebook_id = $" + strconv.Itoa(idx)
	_, err := db.Exec(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update cell"})
		return
	}

	if _, err := db.Exec("UPDATE notebooks SET updated_at = $1 WHERE id = $2", time.Now(), notebookID); err != nil {
		log.Error().Err(err).Msg("failed to update notebook timestamp")
	}

	// Return cell without order — order is managed exclusively by reorderCells
	var cell struct {
		ID                  string          `json:"id"`
		NotebookID          string          `json:"notebook_id"`
		Type                string          `json:"type"`
		Source              string          `json:"source"`
		LastOutput          json.RawMessage `json:"last_output"`
		LastExecutionTimeMs *int            `json:"last_execution_time_ms"`
		ExecutionCount      *int            `json:"execution_count"`
		CreatedAt           time.Time       `json:"created_at"`
		UpdatedAt           time.Time       `json:"updated_at"`
	}
	var ignoredOrder int
	var output []byte
	err = db.QueryRow(
		`SELECT id, notebook_id, type, source, cell_order, last_output, last_execution_time_ms, execution_count, created_at, updated_at
		 FROM notebook_cells WHERE id = $1`, cellID,
	).Scan(&cell.ID, &cell.NotebookID, &cell.Type, &cell.Source, &ignoredOrder,
		&output, &cell.LastExecutionTimeMs, &cell.ExecutionCount, &cell.CreatedAt, &cell.UpdatedAt)
	if err != nil {
		log.Warn().Err(err).Str("cell_id", cellID).Msg("cell updated but failed to fetch updated cell")
		c.JSON(http.StatusOK, gin.H{"message": "cell updated"})
		return
	}
	cell.Type = strings.ToLower(cell.Type)
	if output != nil {
		cell.LastOutput = json.RawMessage(output)
	}
	c.JSON(http.StatusOK, cell)
}

// DeleteCell removes a cell.
func (h *NotebookHandler) DeleteCell(c *gin.Context) {
	cellID := c.Param("cellId")
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	db := database.GetDB()

	result, err := db.Exec("DELETE FROM notebook_cells WHERE id = $1 AND notebook_id = $2", cellID, notebookID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete cell"})
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "cell not found"})
		return
	}

	if _, err := db.Exec("UPDATE notebooks SET updated_at = $1 WHERE id = $2", time.Now(), notebookID); err != nil {
		log.Error().Err(err).Msg("failed to update notebook timestamp")
	}
	c.Status(http.StatusNoContent)
}

// ReorderCells reorders cells in a notebook.
func (h *NotebookHandler) ReorderCells(c *gin.Context) {
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	var req struct {
		CellIDs []string `json:"cell_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	db := database.GetDB()
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	defer tx.Rollback()

	// Batch all reorders into a single UPDATE using unnest — avoids N round trips
	// for notebooks with many cells. Filter out invalid UUIDs (no-op in old loop).
	ids := make([]string, 0, len(req.CellIDs))
	orders := make([]int64, 0, len(req.CellIDs))
	for i, cellID := range req.CellIDs {
		if _, perr := uuid.Parse(cellID); perr != nil {
			log.Warn().Str("cell_id", cellID).Msg("reorder: invalid UUID, skipping")
			continue
		}
		ids = append(ids, cellID)
		orders = append(orders, int64(i+1))
	}
	if len(ids) > 0 {
		res, err := tx.Exec(`
			UPDATE notebook_cells SET cell_order = data.new_order
			FROM unnest($1::uuid[], $2::int[]) AS data(cell_id, new_order)
			WHERE notebook_cells.id = data.cell_id AND notebook_cells.notebook_id = $3
		`, pq.Array(ids), pq.Array(orders), notebookID)
		if err != nil {
			log.Error().Err(err).Msg("failed to reorder cells")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reorder cells"})
			return
		}
		if n, _ := res.RowsAffected(); int(n) != len(ids) {
			log.Warn().Int("expected", len(ids)).Int64("got", n).Str("notebook_id", notebookID).Msg("reorder: some cells not in notebook")
		}
	}
	if _, err := tx.Exec("UPDATE notebooks SET updated_at = $1 WHERE id = $2", time.Now(), notebookID); err != nil {
		log.Error().Err(err).Msg("failed to update notebook timestamp")
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reorder"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "cells reordered"})
}

// KernelSpecs returns available kernel specifications.
func (h *NotebookHandler) KernelSpecs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"default": "python3",
		"kernelspecs": gin.H{
			"python3": gin.H{
				"name":         "python3",
				"display_name": "Python 3",
				"language":     "python",
			},
			"pyspark": gin.H{
				"name":         "pyspark",
				"display_name": "PySpark",
				"language":     "python",
			},
			"scala212": gin.H{
				"name":         "scala212",
				"display_name": "Scala 2.12 (Spark)",
				"language":     "scala",
			},
		},
	})
}
