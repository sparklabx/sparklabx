package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/config"
	"github.com/sparklabx/sparklabx/backend/internal/database"
	"github.com/sparklabx/sparklabx/backend/internal/services"
)

// LocalKernelHandler proxies kernel requests to a Jupyter Kernel Gateway.
// The gateway URL is resolved per-request via KernelGateway — for SharedGateway
// it's a fixed URL (docker-compose), for K8sPerUserGateway it's the per-user
// pod IP (spawned on demand).
type LocalKernelHandler struct {
	gateway  services.KernelGateway
	upgrader websocket.Upgrader
}

func NewLocalKernelHandler(cfg *config.Config, gateway services.KernelGateway) *LocalKernelHandler {
	allowedOrigins := make(map[string]bool)
	for _, o := range cfg.CORSOrigins {
		allowedOrigins[o] = true
	}
	return &LocalKernelHandler{
		gateway: gateway,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				return allowedOrigins[origin]
			},
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
	}
}

// gatewayURLFor resolves the per-user gateway URL. With the async /connect
// flow the pod should already be ready by the time any handler reaches here,
// so we only need a short timeout to cover transient K8s API blips. The
// fallback "spawn from scratch" path in GetGatewayURL still works (for WS /
// ProxyHTTP after a pod evict) but in that rare case the caller eats the 5min.
func (h *LocalKernelHandler) gatewayURLFor(c *gin.Context, userID string) (string, bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	url, err := h.gateway.GetGatewayURL(ctx, userID)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("failed to resolve kernel gateway")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return "", false
	}
	return url, true
}

// SpawnStatus returns the current spawn phase for the caller's per-user pod.
// FE polls this every ~1.5s while /connect is in flight so it can show a live
// progress message ("Pulling image…", "Container starting…") instead of a
// blank spinner that times out 5 minutes later.
//
// GET /api/v1/kernel/spawn-status
func (h *LocalKernelHandler) SpawnStatus(c *gin.Context) {
	userID := userIDString(c)
	if userID == "" {
		c.JSON(http.StatusOK, gin.H{"phase": "", "message": ""})
		return
	}
	st, err := h.gateway.Status(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, st)
}

func userIDString(c *gin.Context) string {
	if v, ok := c.Get("admin_id"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if v, ok := c.Get("user_id"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// kernelMap is an in-memory CACHE of the notebook_kernels Postgres table.
// Source of truth is the DB — kernelMap is reloaded on startup so a backend
// restart doesn't lose student→kernel mappings (students keep their RAM state).
//
// Writes (set/delete): update map AND write through to DB.
// Reads: from map only (no DB hit on hot path).
// Touches: buffered in touchBuf, flushed every 10s (DB-friendly).
var kernelMap = make(map[string]string)
var kernelLastUsed = make(map[string]time.Time)
var kernelMapMu sync.Mutex
var kernelCleanupStarted bool

// Buffered "last_used_at" updates — flushed every 10s by flushKernelTouchLoop.
// Keeps touch off the hot path (WS frame handler calls touch on every message).
var touchBuf = make(map[string]time.Time)
var touchBufMu sync.Mutex

const kernelIdleTimeout = 45 * time.Minute

func kernelKeyForNotebook(notebookID string, userID interface{}) string {
	return fmt.Sprintf("%s:%v", notebookID, userID)
}

// loadKernelMapFromDB hydrates the in-memory cache from notebook_kernels.
// Called once at backend startup so a restart resumes student kernel state
// instead of creating new kernels and losing variables.
func LoadKernelMapFromDB() {
	rows, err := database.GetDB().Query(
		`SELECT notebook_id, user_id, kernel_id, last_used_at FROM notebook_kernels`,
	)
	if err != nil {
		log.Warn().Err(err).Msg("failed to load notebook_kernels from DB")
		return
	}
	defer rows.Close()
	kernelMapMu.Lock()
	defer kernelMapMu.Unlock()
	count := 0
	for rows.Next() {
		var nbID, uid, kid string
		var lastUsed time.Time
		if err := rows.Scan(&nbID, &uid, &kid, &lastUsed); err != nil {
			continue
		}
		key := kernelKeyForNotebook(nbID, uid)
		kernelMap[key] = kid
		kernelLastUsed[key] = lastUsed
		count++
	}
	log.Info().Int("count", count).Msg("loaded notebook_kernels from DB")
}

// setKernelMap updates the in-memory cache AND persists to DB.
// Call site convention: caller holds NO lock — this function handles its own.
func setKernelMap(notebookID, userID, kernelID string) {
	key := kernelKeyForNotebook(notebookID, userID)
	now := time.Now()
	kernelMapMu.Lock()
	kernelMap[key] = kernelID
	kernelLastUsed[key] = now
	kernelMapMu.Unlock()
	_, err := database.GetDB().Exec(
		`INSERT INTO notebook_kernels (notebook_id, user_id, kernel_id, last_used_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (notebook_id, user_id) DO UPDATE SET kernel_id = $3, last_used_at = $4`,
		notebookID, userID, kernelID, now,
	)
	if err != nil {
		log.Warn().Err(err).Str("notebook_id", notebookID).Msg("failed to persist kernel mapping")
	}
}

// deleteKernelMap removes from cache AND DB.
func deleteKernelMap(notebookID, userID string) {
	key := kernelKeyForNotebook(notebookID, userID)
	kernelMapMu.Lock()
	delete(kernelMap, key)
	delete(kernelLastUsed, key)
	kernelMapMu.Unlock()
	touchBufMu.Lock()
	delete(touchBuf, key)
	touchBufMu.Unlock()
	_, err := database.GetDB().Exec(
		`DELETE FROM notebook_kernels WHERE notebook_id = $1 AND user_id = $2`,
		notebookID, userID,
	)
	if err != nil {
		log.Warn().Err(err).Str("notebook_id", notebookID).Msg("failed to delete kernel mapping")
	}
}

// touchKernel buffers a last_used_at update — flushed every 10s.
// Cheap (in-memory map insert) so safe to call from WS hot paths.
func touchKernel(notebookID, userID string) {
	key := kernelKeyForNotebook(notebookID, userID)
	now := time.Now()
	kernelMapMu.Lock()
	kernelLastUsed[key] = now
	kernelMapMu.Unlock()
	touchBufMu.Lock()
	touchBuf[key] = now
	touchBufMu.Unlock()
}

// touchKernelLastUsed kept as backward-compat shim — same as touchKernel but
// takes the legacy "notebookID:userID" key form.
func touchKernelLastUsed(kernelKey string) {
	parts := strings.SplitN(kernelKey, ":", 2)
	if len(parts) != 2 {
		return
	}
	touchKernel(parts[0], parts[1])
}

// flushKernelTouchLoop periodically writes buffered touches to DB.
// One batched UPDATE per 10s instead of one UPDATE per WS frame.
func flushKernelTouchLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		touchBufMu.Lock()
		if len(touchBuf) == 0 {
			touchBufMu.Unlock()
			continue
		}
		snap := touchBuf
		touchBuf = make(map[string]time.Time)
		touchBufMu.Unlock()
		db := database.GetDB()
		for key, ts := range snap {
			parts := strings.SplitN(key, ":", 2)
			if len(parts) != 2 {
				continue
			}
			db.Exec(
				`UPDATE notebook_kernels SET last_used_at = $1
				 WHERE notebook_id = $2 AND user_id = $3`,
				ts, parts[0], parts[1],
			)
		}
	}
}

func kernelIDFromAPIPath(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "kernels" && parts[1] != "" {
		return parts[1], true
	}
	return "", false
}

// startKernelCleanup runs a background goroutine that kills idle kernels.
// Per-user pod reaping is handled separately by K8sPerUserGateway.reaperLoop.
// This loop only kills individual kernels (kernel_id) inside whichever gateway
// they live in — useful for both shared and per-user modes since users may have
// many notebooks per pod.
func startKernelCleanup(gw services.KernelGateway) {
	if kernelCleanupStarted {
		return
	}
	kernelCleanupStarted = true
	go flushKernelTouchLoop() // batched DB touch writer
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			kernelMapMu.Lock()
			now := time.Now()
			type victim struct{ key, notebookID, userID, kernelID string }
			var victims []victim
			for key, lastUsed := range kernelLastUsed {
				if now.Sub(lastUsed) > kernelIdleTimeout {
					nbID, uid := parseKernelKey(key)
					victims = append(victims, victim{key: key, notebookID: nbID, userID: uid, kernelID: kernelMap[key]})
				}
			}
			kernelMapMu.Unlock()

			// Drop both cache + DB row for each victim, then kill kernel on gateway.
			for _, v := range victims {
				deleteKernelMap(v.notebookID, v.userID)
				if v.kernelID == "" {
					continue
				}
				url, err := gw.GetGatewayURL(context.Background(), v.userID)
				if err != nil {
					continue // pod gone, kernel gone with it
				}
				req, _ := http.NewRequest("DELETE", url+"/api/kernels/"+v.kernelID, nil)
				if req != nil {
					http.DefaultClient.Do(req)
				}
				log.Info().Str("kernel_id", v.kernelID).Str("key", v.key).Msg("idle kernel cleaned up")
			}
		}
	}()
}

// parseKernelKey splits "notebookID:userID" back into its parts.
func parseKernelKey(key string) (notebookID, userID string) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}


// Connect creates or reuses a kernel session on the user's gateway.
//
// Async-friendly contract:
//   - 200 + {kernel_id}        — pod ready, kernel session created. FE opens WS.
//   - 202 + {phase, message}   — pod still spawning. FE polls /spawn-status and
//                                 retries this endpoint when phase reaches 'ready'.
//   - 503 + {error}            — hard failure (capacity, K8s API down). FE shows error.
//
// SharedGateway never returns 202 (EnsureSpawning is no-op, Status is always
// 'ready'), so docker-compose flow is unchanged.
//
// POST /api/v1/notebooks/:id/kernel/connect
func (h *LocalKernelHandler) Connect(c *gin.Context) {
	startKernelCleanup(h.gateway)

	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	userID := userIDString(c)
	kernelKey := kernelKeyForNotebook(notebookID, userID)

	var req struct {
		Language   string `json:"language"`
		KernelName string `json:"kernel_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	kernelName := req.KernelName
	if kernelName == "" {
		switch req.Language {
		case "scala":
			kernelName = "scala212"
		default:
			kernelName = "pyspark"
		}
	}

	// If a previous pod for this user is mid-shutdown, DON'T stack a fresh
	// spawn on top — the new pod would just sit waiting for the predecessor
	// to die, and FE would see "connecting…" with no signal that something's
	// off. Instead return 202 immediately so FE polls + retries when the
	// terminating row is cleared by the background Destroy goroutine.
	if st, _ := h.gateway.Status(userID); st.Phase == services.PhaseTerminating {
		c.JSON(http.StatusAccepted, gin.H{
			"status":  "spawning",
			"phase":   st.Phase,
			"message": st.Message,
		})
		return
	}

	// Non-blocking spawn trigger. Returns immediately; in k8s_per_user mode
	// this kicks off a goroutine that updates DB phase as the pod progresses.
	if err := h.gateway.EnsureSpawning(userID); err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("ensure spawning failed")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}

	// If pod isn't ready yet, tell FE to poll instead of blocking the HTTP call.
	st, _ := h.gateway.Status(userID)
	if st.Phase != "" && st.Phase != services.PhaseReady {
		c.JSON(http.StatusAccepted, gin.H{
			"status":  "spawning",
			"phase":   st.Phase,
			"message": st.Message,
		})
		return
	}

	gatewayURL, ok := h.gatewayURLFor(c, userID)
	if !ok {
		return
	}
	h.gateway.Touch(userID)

	// Check if this notebook already has a kernel
	kernelMapMu.Lock()
	existingKernelID := kernelMap[kernelKey]
	kernelMapMu.Unlock()

	if existingKernelID != "" {
		// Verify kernel is still alive
		checkResp, err := http.Get(gatewayURL + "/api/kernels/" + existingKernelID)
		if err == nil && checkResp.StatusCode == 200 {
			checkResp.Body.Close()
			touchKernelLastUsed(kernelKey)
			log.Info().Str("kernel_id", existingKernelID).Str("notebook_id", notebookID).Msg("reusing kernel for notebook")
			c.JSON(http.StatusOK, gin.H{
				"kernel_id":   existingKernelID,
				"kernel_name": kernelName,
				"language":    req.Language,
				"status":      "connected",
			})
			return
		}
		if checkResp != nil {
			checkResp.Body.Close()
		}
		// Kernel dead — remove from map + DB
		deleteKernelMap(notebookID, userID)
	}

	// Create new kernel
	bodyBytes, _ := json.Marshal(map[string]string{"name": kernelName})
	resp, err := http.Post(gatewayURL+"/api/kernels", "application/json", strings.NewReader(string(bodyBytes)))
	if err != nil {
		// Transport-level failure (EOF, connection reset, refused, timeout).
		// The DB has a "ready" pod row pointing at a container that isn't
		// actually serving — almost always because the container restarted
		// and Jupyter is gone, or the container was rm'd out from under us.
		// Destroy the stale row + container so the next /kernel/connect
		// from the FE triggers a fresh spawn instead of looping forever.
		log.Error().Err(err).Str("user_id", userID).Msg("kernel gateway unreachable; treating pod as stale, destroying for respawn")
		if dErr := h.gateway.Destroy(userID); dErr != nil {
			log.Warn().Err(dErr).Str("user_id", userID).Msg("destroy stale pod failed")
		}
		deleteKernelMap(notebookID, userID)
		// 503 with phase=spawning tells FE "container is being rebuilt, poll".
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":  "spawning",
			"phase":   services.PhaseSpawning,
			"message": "kernel container was stale; respawning",
		})
		return
	}
	defer resp.Body.Close()

	var result struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid response from gateway"})
		return
	}

	// Store kernel ID for this notebook (cache + DB)
	setKernelMap(notebookID, userID, result.ID)

	log.Info().Str("kernel_id", result.ID).Str("name", result.Name).Str("notebook_id", notebookID).Msg("kernel created for notebook")
	c.JSON(http.StatusOK, gin.H{
		"kernel_id":   result.ID,
		"kernel_name": result.Name,
		"language":    req.Language,
		"status":      "connected",
	})
}

// Status checks if a kernel session exists.
// GET /api/v1/notebooks/:id/kernel/status?kernel_name=pyspark
func (h *LocalKernelHandler) Status(c *gin.Context) {
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	userID := userIDString(c)
	kernelKey := kernelKeyForNotebook(notebookID, userID)

	// Check if this notebook+user has a kernel in our map
	kernelMapMu.Lock()
	existingKernelID := kernelMap[kernelKey]
	kernelMapMu.Unlock()

	if existingKernelID == "" {
		c.JSON(http.StatusOK, gin.H{"status": "disconnected"})
		return
	}

	gatewayURL, ok := h.gatewayURLFor(c, userID)
	if !ok {
		return
	}
	// Verify kernel is still alive on gateway
	resp, err := http.Get(gatewayURL + "/api/kernels/" + existingKernelID)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		// Kernel dead — clean up cache + DB
		deleteKernelMap(notebookID, userID)
		c.JSON(http.StatusOK, gin.H{"status": "disconnected"})
		return
	}
	defer resp.Body.Close()

	var kernel struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&kernel)

	// Check kernel_name filter
	wantName := c.Query("kernel_name")
	if wantName != "" && kernel.Name != wantName {
		c.JSON(http.StatusOK, gin.H{"status": "disconnected"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":      "connected",
		"kernel_id":   kernel.ID,
		"kernel_name": kernel.Name,
	})
}

// Disconnect closes the WebSocket but keeps the kernel alive.
// DELETE /api/v1/notebooks/:id/kernel/disconnect
func (h *LocalKernelHandler) Disconnect(c *gin.Context) {
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	userID := userIDString(c)
	kernelKey := kernelKeyForNotebook(notebookID, userID)

	kernelMapMu.Lock()
	kernelID := kernelMap[kernelKey]
	kernelMapMu.Unlock()

	log.Info().Str("kernel_id", kernelID).Str("key", kernelKey).Msg("kernel disconnected (kept alive)")
	c.JSON(http.StatusOK, gin.H{"message": "disconnected", "kernel_kept": true})
}

// Shutdown kills the kernel for this notebook+user (full reset).
// DELETE /api/v1/notebooks/:id/kernel/shutdown
func (h *LocalKernelHandler) Shutdown(c *gin.Context) {
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	userID := userIDString(c)
	kernelKey := kernelKeyForNotebook(notebookID, userID)

	kernelMapMu.Lock()
	kernelID := kernelMap[kernelKey]
	kernelMapMu.Unlock()

	if kernelID != "" {
		deleteKernelMap(notebookID, userID)
		gatewayURL, ok := h.gatewayURLFor(c, userID)
		if !ok {
			return
		}
		req, _ := http.NewRequest("DELETE", gatewayURL+"/api/kernels/"+kernelID, nil)
		if req != nil {
			http.DefaultClient.Do(req)
		}
		log.Info().Str("kernel_id", kernelID).Str("key", kernelKey).Msg("kernel shutdown")
	}

	// If this was the user's last active kernel, also tear down their dedicated
	// gateway (pod). For SharedGateway this is a no-op; for K8sPerUserGateway
	// it frees cluster resources immediately instead of waiting ~30min for the
	// idle reaper.
	if userID != "" && !h.userHasOtherKernels(userID) {
		if err := h.gateway.Destroy(userID); err != nil {
			log.Warn().Err(err).Str("user_id", userID).Msg("failed to destroy user gateway on last-kernel shutdown")
		} else {
			log.Info().Str("user_id", userID).Msg("destroyed user gateway (last kernel)")
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "kernel shutdown"})
}

// userHasOtherKernels returns true if the user has any kernels left in the
// in-memory map across all their notebooks.
func (h *LocalKernelHandler) userHasOtherKernels(userID string) bool {
	suffix := ":" + userID
	kernelMapMu.Lock()
	defer kernelMapMu.Unlock()
	for key, kid := range kernelMap {
		if kid != "" && strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

// WebSocket proxies WebSocket connections to the user's kernel gateway.
// ANY /api/v1/notebooks/:id/kernel/ws/:kernelId/*path
func (h *LocalKernelHandler) WebSocket(c *gin.Context) {
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	kernelID := c.Param("kernelId")

	// Update lastUsed for idle timeout tracking
	userID := userIDString(c)
	kernelKey := kernelKeyForNotebook(notebookID, userID)
	kernelMapMu.Lock()
	expectedKernelID := kernelMap[kernelKey]
	kernelMapMu.Unlock()
	if expectedKernelID == "" || expectedKernelID != kernelID {
		c.JSON(http.StatusForbidden, gin.H{"error": "kernel does not belong to this notebook"})
		return
	}
	touchKernelLastUsed(kernelKey)
	h.gateway.Touch(userID)

	gatewayURL, ok := h.gatewayURLFor(c, userID)
	if !ok {
		return
	}

	// Build target WebSocket URL
	wsURL := strings.Replace(gatewayURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)

	jupyterPath := c.Param("path")
	if jupyterPath == "" {
		jupyterPath = "/channels"
	}
	targetURL := fmt.Sprintf("%s/api/kernels/%s%s", wsURL, kernelID, jupyterPath)

	// Don't forward query params (contains JWT token, Jupyter doesn't need it)

	logger := log.With().Str("kernel_id", kernelID).Str("target", targetURL).Logger()

	// Connect to gateway (longer timeout for emulated containers on ARM)
	dialer := websocket.Dialer{HandshakeTimeout: 60 * time.Second}
	backendConn, _, err := dialer.Dial(targetURL, nil)
	if err != nil {
		logger.Error().Err(err).Msg("failed to connect to gateway WebSocket")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to connect to kernel"})
		return
	}
	defer backendConn.Close()

	// Upgrade client
	clientConn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Error().Err(err).Msg("failed to upgrade client WebSocket")
		return
	}
	defer clientConn.Close()

	logger.Info().Msg("local kernel WebSocket proxy established")

	errChan := make(chan error, 2)

	go func() {
		for {
			msgType, msg, err := clientConn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			touchKernelLastUsed(kernelKey)
			if err := backendConn.WriteMessage(msgType, msg); err != nil {
				errChan <- err
				return
			}
			touchKernelLastUsed(kernelKey)
		}
	}()

	go func() {
		for {
			msgType, msg, err := backendConn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			touchKernelLastUsed(kernelKey)
			if err := clientConn.WriteMessage(msgType, msg); err != nil {
				errChan <- err
				return
			}
			touchKernelLastUsed(kernelKey)
		}
	}()

	err = <-errChan
	if err != nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		logger.Debug().Err(err).Msg("local kernel WebSocket proxy closed")
	}
}

// ProxyHTTP proxies REST API calls to the user's kernel gateway.
// ANY /api/v1/notebooks/:id/kernel/api/*path
func (h *LocalKernelHandler) ProxyHTTP(c *gin.Context) {
	notebookID := c.Param("id")
	if !checkNotebookWriteAccess(c, notebookID) {
		return
	}
	userID := userIDString(c)
	kernelKey := kernelKeyForNotebook(notebookID, userID)
	kernelMapMu.Lock()
	expectedKernelID := kernelMap[kernelKey]
	kernelMapMu.Unlock()

	path := c.Param("path")
	if kernelID, ok := kernelIDFromAPIPath(path); ok && (expectedKernelID == "" || kernelID != expectedKernelID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "kernel does not belong to this notebook"})
		return
	}
	h.gateway.Touch(userID)
	gatewayURL, ok := h.gatewayURLFor(c, userID)
	if !ok {
		return
	}
	targetURL := gatewayURL + "/api" + path

	if c.Request.URL.RawQuery != "" {
		targetURL += "?" + c.Request.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, targetURL, c.Request.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create proxy request"})
		return
	}

	for k, v := range c.Request.Header {
		req.Header[k] = v
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to reach Jupyter Gateway"})
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		for _, vv := range v {
			c.Writer.Header().Add(k, vv)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)
	io.Copy(c.Writer, resp.Body)
}
