package services

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

// Usage metrics: who is online, kernels and resources in use, per-user daily totals.
// Everything is accumulated by one in-process collector tick per minute (single
// backend replica), so there is no extra infrastructure to run.

var (
	cellsMu  sync.Mutex
	cellRuns = map[string]int{} // admin id -> execute_requests since the last tick
)

// RecordCellRun counts one cell execution (called from the kernel WebSocket proxy).
func RecordCellRun(userID string) {
	if userID == "" {
		return
	}
	cellsMu.Lock()
	cellRuns[userID]++
	cellsMu.Unlock()
}

func takeCellRuns() map[string]int {
	cellsMu.Lock()
	defer cellsMu.Unlock()
	out := cellRuns
	cellRuns = map[string]int{}
	return out
}

// StartUsageCollector runs the per-minute accumulation in the background.
func StartUsageCollector(kernels KernelGateway) {
	go func() {
		for range time.Tick(time.Minute) {
			if err := collectUsage(kernels); err != nil {
				log.Warn().Err(err).Msg("usage collection failed")
			}
		}
	}()
}

type minuteUsage struct {
	active, kernel bool
	cpuSec, memGBs float64
	cells          int
}

func collectUsage(kernels KernelGateway) error {
	db := database.GetDB()
	now := time.Now().UTC()
	per := map[string]*minuteUsage{}
	get := func(id string) *minuteUsage {
		if per[id] == nil {
			per[id] = &minuteUsage{}
		}
		return per[id]
	}

	// Active this minute = an authenticated request in the last 60s.
	rows, err := db.Query(`SELECT id::text FROM admins WHERE last_seen_at > NOW() - interval '60 seconds'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			get(id).active = true
		}
	}
	rows.Close()

	// Running kernels + their live CPU/RAM (metrics-server via the gateway, cached ~15s).
	var totalCPU float64
	var totalMem int64
	var kernelCount int
	rows, err = db.Query(`SELECT user_id FROM user_kernel_pods WHERE status = 'ready'`)
	if err != nil {
		return err
	}
	var kernelUsers []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			kernelUsers = append(kernelUsers, id)
		}
	}
	rows.Close()
	for _, id := range kernelUsers {
		kernelCount++
		u := get(id)
		u.kernel = true
		if kernels == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ru, err := kernels.Usage(ctx, id)
		cancel()
		if err == nil && ru.MetricsLive {
			u.cpuSec = ru.CPUUsedCores * 60
			u.memGBs = float64(ru.MemUsedBytes) / 1e9 * 60
			totalCPU += ru.CPUUsedCores
			totalMem += ru.MemUsedBytes
		}
	}
	for id, n := range takeCellRuns() {
		get(id).cells += n
	}

	day := now.Format("2006-01-02")
	for id, u := range per {
		_, err := db.Exec(`
			INSERT INTO usage_daily (user_id, day, active_minutes, kernel_minutes, cpu_core_seconds, mem_gb_seconds, cells_run)
			SELECT $1::uuid, $2::date, $3, $4, $5, $6, $7 WHERE EXISTS (SELECT 1 FROM admins WHERE id = $1::uuid)
			ON CONFLICT (user_id, day) DO UPDATE SET
				active_minutes   = usage_daily.active_minutes + EXCLUDED.active_minutes,
				kernel_minutes   = usage_daily.kernel_minutes + EXCLUDED.kernel_minutes,
				cpu_core_seconds = usage_daily.cpu_core_seconds + EXCLUDED.cpu_core_seconds,
				mem_gb_seconds   = usage_daily.mem_gb_seconds + EXCLUDED.mem_gb_seconds,
				cells_run        = usage_daily.cells_run + EXCLUDED.cells_run`,
			id, day, b2i(u.active), b2i(u.kernel), u.cpuSec, u.memGBs, u.cells)
		if err != nil {
			log.Warn().Err(err).Str("user", id).Msg("usage_daily upsert failed")
		}
	}

	var online int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admins WHERE last_seen_at > NOW() - interval '5 minutes'`).Scan(&online); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO usage_snapshots (ts, online_users, running_kernels, cpu_cores, mem_bytes)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (ts) DO NOTHING`,
		now.Truncate(time.Minute), online, kernelCount, totalCPU, totalMem); err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM usage_snapshots WHERE ts < NOW() - interval '30 days'`)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
