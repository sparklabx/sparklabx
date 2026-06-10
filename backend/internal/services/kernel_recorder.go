// Package services — KernelRecorder maintains a persistent WebSocket
// to Jupyter Kernel Gateway per kernel, independent of any client. It
// reads iopub + shell messages, tags each by the cell it belongs to
// (via msg_id → cell_id mapping registered by the per-client WS proxy
// on execute_request), and writes accumulated output to the
// notebook_cells table.
//
// This is the foundation for "tab reload preserves running cell" UX:
// the kernel keeps emitting messages even after the user's WS drops,
// and the recorder captures them all instead of dropping into the
// void because JKG has no subscriber.
package services

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

const (
	recorderFlushInterval = 500 * time.Millisecond
	recorderRetryBackoff  = 2 * time.Second
)

// CellOutput mirrors the frontend's CellOutput shape so the JSON we
// persist to cell.last_output matches what the FE expects when it
// reads the cell back from DB on mount.
type CellOutput struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Ename     string                 `json:"ename,omitempty"`
	Evalue    string                 `json:"evalue,omitempty"`
	Traceback []string               `json:"traceback,omitempty"`
}

// ExecutionRecord is the per-execute_request state the recorder
// builds up as iopub messages arrive. One record lives for the
// lifetime of one execution; a fresh execution of the same cell
// creates a new record and supersedes the previous via cellLatest.
type ExecutionRecord struct {
	MsgID           string       `json:"msg_id"`
	CellID          string       `json:"cell_id"`
	StartedAt       time.Time    `json:"started_at"`
	Completed       bool         `json:"completed"`
	HasError        bool         `json:"has_error"`
	Outputs         []CellOutput `json:"-"`
	ExecutionCount  int          `json:"execution_count"`
	ExecutionTimeMs int64        `json:"execution_time_ms"`
}

// KernelRecorder is a singleton per kernel_id. Its WS to JKG stays
// open for the life of the kernel pod (independent of clients).
type KernelRecorder struct {
	NotebookID string
	KernelID   string
	WSURL      string

	conn        *websocket.Conn
	connWriteMu sync.Mutex

	mu         sync.Mutex
	records    map[string]*ExecutionRecord // msg_id → record
	cellLatest map[string]string           // cell_id → latest msg_id (newer runs supersede)
	dirty      map[string]bool             // cell_id → needs flush

	stopCh   chan struct{}
	stopOnce sync.Once
	stopped  bool
}

// ── Registry ──────────────────────────────────────────────────────

var (
	recorders   = make(map[string]*KernelRecorder)
	recordersMu sync.Mutex
)

// GetOrCreateRecorder returns the existing recorder for kernelID or
// starts a new one. wsURL is the JKG channels endpoint
// (ws://gateway/api/kernels/<id>/channels).
func GetOrCreateRecorder(notebookID, kernelID, wsURL string) (*KernelRecorder, error) {
	recordersMu.Lock()
	defer recordersMu.Unlock()
	if existing, ok := recorders[kernelID]; ok && !existing.stopped {
		return existing, nil
	}
	r := &KernelRecorder{
		NotebookID: notebookID,
		KernelID:   kernelID,
		WSURL:      wsURL,
		records:    make(map[string]*ExecutionRecord),
		cellLatest: make(map[string]string),
		dirty:      make(map[string]bool),
		stopCh:     make(chan struct{}),
	}
	if err := r.dial(); err != nil {
		return nil, err
	}
	recorders[kernelID] = r
	go r.readLoop()
	go r.flushLoop()
	log.Info().Str("kernel", kernelID).Msg("KernelRecorder: started")
	return r, nil
}

// StopRecorder closes the recorder for kernelID. Called when the
// kernel itself is shut down (handler) or reaped (idle cleanup).
func StopRecorder(kernelID string) {
	recordersMu.Lock()
	r := recorders[kernelID]
	delete(recorders, kernelID)
	recordersMu.Unlock()
	if r != nil {
		r.stop()
	}
}

// GetRecorder returns the recorder for kernelID without creating
// one. Returns nil if no recorder is registered.
func GetRecorder(kernelID string) *KernelRecorder {
	recordersMu.Lock()
	defer recordersMu.Unlock()
	if r, ok := recorders[kernelID]; ok && !r.stopped {
		return r
	}
	return nil
}

// ── Lifecycle ─────────────────────────────────────────────────────

func (r *KernelRecorder) dial() error {
	dialer := websocket.Dialer{HandshakeTimeout: 30 * time.Second}
	conn, _, err := dialer.Dial(r.WSURL, nil)
	if err != nil {
		return err
	}
	r.conn = conn
	return nil
}

func (r *KernelRecorder) stop() {
	r.stopOnce.Do(func() {
		r.stopped = true
		close(r.stopCh)
		if r.conn != nil {
			_ = r.conn.Close()
		}
		// Final flush so any pending writes land.
		r.flushOnce()
		log.Info().Str("kernel", r.KernelID).Msg("KernelRecorder: stopped")
	})
}

// readLoop pumps messages from the kernel into ExecutionRecords.
// Exits on stop or unrecoverable WS read error.
func (r *KernelRecorder) readLoop() {
	defer r.stop()
	for {
		select {
		case <-r.stopCh:
			return
		default:
		}
		_, msg, err := r.conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Debug().Err(err).Str("kernel", r.KernelID).Msg("KernelRecorder: read error")
			}
			return
		}
		r.ingest(msg)
	}
}

// flushLoop wakes every recorderFlushInterval, batches dirty cells'
// state into DB writes. One UPDATE per dirty cell per tick — cheap.
func (r *KernelRecorder) flushLoop() {
	t := time.NewTicker(recorderFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-t.C:
			r.flushOnce()
		}
	}
}

// ── Public API: register + query ──────────────────────────────────

// RegisterExecution is called by the per-client WS proxy when it sees
// an execute_request go by, carrying metadata.sparklabx_cell_id so we
// can tag iopub messages by cell. Without this mapping the recorder
// would still buffer outputs but couldn't tell which DB row to write
// them to.
func (r *KernelRecorder) RegisterExecution(msgID, cellID string) {
	if msgID == "" || cellID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.records[msgID]; exists {
		return // idempotent — duplicate registration from retry, etc.
	}
	r.records[msgID] = &ExecutionRecord{
		MsgID:     msgID,
		CellID:    cellID,
		StartedAt: time.Now(),
	}
	r.cellLatest[cellID] = msgID
}

// ActiveExecutions returns a snapshot of all in-flight executions
// for which we know the cell_id. Used by the active-executions
// endpoint to let the FE rebuild runningCells on mount.
func (r *KernelRecorder) ActiveExecutions() []ExecutionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []ExecutionRecord{}
	for _, rec := range r.records {
		if rec.CellID == "" || rec.Completed {
			continue
		}
		out = append(out, *rec) // copy by value
	}
	return out
}

// ── Internal: message ingestion ───────────────────────────────────

func (r *KernelRecorder) ingest(raw []byte) {
	var msg struct {
		Channel      string                 `json:"channel"`
		Header       msgHeader              `json:"header"`
		ParentHeader msgHeader              `json:"parent_header"`
		Content      map[string]interface{} `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return // unparseable — drop silently
	}
	parentID := msg.ParentHeader.MsgID
	if parentID == "" {
		return // can't attribute to an execution
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.records[parentID]
	if rec == nil {
		return // parent execution not registered (e.g. backend restarted, lost map)
	}

	switch msg.Header.MsgType {
	case "stream":
		text, _ := msg.Content["text"].(string)
		rec.Outputs = append(rec.Outputs, CellOutput{Type: "stream", Text: text})

	case "display_data", "execute_result":
		data, _ := msg.Content["data"].(map[string]interface{})
		rec.Outputs = append(rec.Outputs, CellOutput{Type: "result", Data: data})
		if cnt, ok := msg.Content["execution_count"].(float64); ok {
			rec.ExecutionCount = int(cnt)
		}

	case "error":
		ename, _ := msg.Content["ename"].(string)
		evalue, _ := msg.Content["evalue"].(string)
		var tb []string
		if raw, ok := msg.Content["traceback"].([]interface{}); ok {
			for _, t := range raw {
				if s, ok := t.(string); ok {
					tb = append(tb, s)
				}
			}
		}
		rec.HasError = true
		rec.Outputs = append(rec.Outputs, CellOutput{
			Type: "error", Ename: ename, Evalue: evalue, Traceback: tb,
		})

	case "execute_input":
		// Earliest message in a cycle, carries the In[N] number. We
		// can't get this from execute_reply because that's a shell
		// channel point-to-point reply and our recorder has its
		// own session (only iopub broadcasts reach us).
		if cnt, ok := msg.Content["execution_count"].(float64); ok {
			rec.ExecutionCount = int(cnt)
		}

	case "status":
		// iopub status transitions are the only completion signal
		// the recorder sees — shell channel's execute_reply only
		// goes back to the requesting session (the proxy WS), not
		// us. status:idle with parent = some execute_request means
		// THAT execution just finished.
		state, _ := msg.Content["execution_state"].(string)
		if state == "idle" {
			rec.Completed = true
			rec.ExecutionTimeMs = time.Since(rec.StartedAt).Milliseconds()
		} else {
			return // busy / starting — no DB write needed
		}

	case "clear_output":
		// Some libraries (tqdm in widget mode) emit this to wipe previous
		// output before re-rendering. Mirror that here so DB doesn't
		// keep growing.
		rec.Outputs = rec.Outputs[:0]

	default:
		return // comm_*, kernel_info_reply, etc. — not output
	}

	r.dirty[rec.CellID] = true
}

type msgHeader struct {
	MsgID   string `json:"msg_id"`
	MsgType string `json:"msg_type"`
}

// ── Internal: DB flush ────────────────────────────────────────────

// flushOnce drains the dirty set and writes each affected cell's
// latest record to notebook_cells.last_output (+ time/count when
// completed). Lock-free for the SQL phase — we copy state out under
// the mutex then release before hitting Postgres.
func (r *KernelRecorder) flushOnce() {
	r.mu.Lock()
	if len(r.dirty) == 0 {
		r.mu.Unlock()
		return
	}
	type pending struct {
		cellID  string
		outputs []byte
		execMs  *int64
		execCnt *int
	}
	work := make([]pending, 0, len(r.dirty))
	for cellID := range r.dirty {
		msgID := r.cellLatest[cellID]
		if msgID == "" {
			continue
		}
		rec := r.records[msgID]
		if rec == nil {
			continue
		}
		payload := map[string]interface{}{
			"outputs":  rec.Outputs,
			"executed": rec.Completed,
		}
		if rec.Outputs == nil {
			payload["outputs"] = []CellOutput{}
		}
		b, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		p := pending{cellID: cellID, outputs: b}
		if rec.Completed {
			ms := rec.ExecutionTimeMs
			cnt := rec.ExecutionCount
			p.execMs = &ms
			if cnt > 0 {
				p.execCnt = &cnt
			}
		}
		work = append(work, p)
	}
	r.dirty = make(map[string]bool)
	r.mu.Unlock()

	db := database.GetDB()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, p := range work {
		// Three forms: in-progress (just output), completed (+time+count), completed without exec_count yet
		var err error
		switch {
		case p.execMs != nil && p.execCnt != nil:
			_, err = db.ExecContext(ctx,
				`UPDATE notebook_cells SET last_output = $1, last_execution_time_ms = $2, execution_count = $3, updated_at = NOW() WHERE id = $4 AND notebook_id = $5`,
				p.outputs, *p.execMs, *p.execCnt, p.cellID, r.NotebookID)
		case p.execMs != nil:
			_, err = db.ExecContext(ctx,
				`UPDATE notebook_cells SET last_output = $1, last_execution_time_ms = $2, updated_at = NOW() WHERE id = $3 AND notebook_id = $4`,
				p.outputs, *p.execMs, p.cellID, r.NotebookID)
		default:
			_, err = db.ExecContext(ctx,
				`UPDATE notebook_cells SET last_output = $1, updated_at = NOW() WHERE id = $2 AND notebook_id = $3`,
				p.outputs, p.cellID, r.NotebookID)
		}
		if err != nil {
			log.Warn().Err(err).Str("cell", p.cellID).Msg("KernelRecorder: flush failed")
		}
	}
}
