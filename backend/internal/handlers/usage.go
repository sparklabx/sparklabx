package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sparklabx/sparklabx/backend/internal/database"
	"github.com/sparklabx/sparklabx/backend/internal/services"
)

// UsageHandler serves the superadmin Usage page: concurrency now and over time,
// trial engagement, and per-user activity with abuse flags. Data comes from the
// per-minute collector (services.StartUsageCollector).
type UsageHandler struct {
	kernels services.KernelGateway
}

func NewUsageHandler(kernels services.KernelGateway) *UsageHandler {
	return &UsageHandler{kernels: kernels}
}

// Abuse-flag thresholds (7-day window).
const (
	flagHeavyCPUCoreHours = 10.0 // total CPU
	flagHighAvgCores      = 1.5  // average cores while a kernel runs (mining-like)
	flagIdleKernelMinutes = 120  // kernel up this long ...
	flagIdleRatio         = 3    // ... and > 3x the user's active minutes
)

type usagePoint struct {
	TS       time.Time `json:"ts"`
	Online   int       `json:"online"`
	Kernels  int       `json:"kernels"`
	CPUCores float64   `json:"cpu_cores"`
	MemBytes int64     `json:"mem_bytes"`
}

type usageUser struct {
	ID              string     `json:"id"`
	Username        string     `json:"username"`
	Email           string     `json:"email"`
	Role            string     `json:"role"`
	Status          string     `json:"status"`
	AccessExpiresAt *time.Time `json:"access_expires_at"`
	AccessHours     *float64   `json:"access_hours"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeenAt      *time.Time `json:"last_seen_at"`
	Online          bool       `json:"online"`
	KernelRunning   bool       `json:"kernel_running"`
	CPUNow          float64    `json:"cpu_now"`
	MemNow          int64      `json:"mem_now"`
	TodayActiveMin  int        `json:"today_active_minutes"`
	ActiveDays7d    int        `json:"active_days_7d"`
	ActiveMin7d     int        `json:"active_minutes_7d"`
	KernelMin7d     int        `json:"kernel_minutes_7d"`
	CPUCoreHours7d  float64    `json:"cpu_core_hours_7d"`
	Cells7d         int        `json:"cells_7d"`
	CellsTotal      int        `json:"cells_total"`
	ActiveDaysTotal int        `json:"active_days_total"`
	Flags           []string   `json:"flags"`
}

// Get returns the Usage page data. ?range=24h (default, 5-min buckets) | 7d (hourly).
// GET /api/v1/admin/usage   (superadmin)
func (h *UsageHandler) Get(c *gin.Context) {
	db := database.GetDB()
	bucket, since := 300, "24 hours"
	if c.Query("range") == "7d" {
		bucket, since = 3600, "7 days"
	}

	// Peak per bucket, so short spikes of concurrency stay visible.
	series := []usagePoint{}
	rows, err := db.Query(`
		SELECT to_timestamp(floor(extract(epoch FROM ts) / $1) * $1) AS b,
		       MAX(online_users), MAX(running_kernels), MAX(cpu_cores), MAX(mem_bytes)
		FROM usage_snapshots WHERE ts > NOW() - $2::interval
		GROUP BY b ORDER BY b`, bucket, since)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load usage series"})
		return
	}
	for rows.Next() {
		var p usagePoint
		if rows.Scan(&p.TS, &p.Online, &p.Kernels, &p.CPUCores, &p.MemBytes) == nil {
			series = append(series, p)
		}
	}
	rows.Close()

	users := []usageUser{}
	rows, err = db.Query(`
		SELECT a.id, a.username, a.email, COALESCE(a.role, 'admin'), a.status, a.access_expires_at, a.access_hours, a.created_at, a.last_seen_at,
		       COALESCE(a.last_seen_at > NOW() - interval '5 minutes', false),
		       EXISTS (SELECT 1 FROM user_kernel_pods p WHERE p.user_id = a.id::text AND p.status = 'ready'),
		       COALESCE(SUM(d.active_minutes) FILTER (WHERE d.day = CURRENT_DATE), 0),
		       COUNT(*) FILTER (WHERE d.day > CURRENT_DATE - 7 AND d.active_minutes > 0),
		       COALESCE(SUM(d.active_minutes) FILTER (WHERE d.day > CURRENT_DATE - 7), 0),
		       COALESCE(SUM(d.kernel_minutes) FILTER (WHERE d.day > CURRENT_DATE - 7), 0),
		       COALESCE(SUM(d.cpu_core_seconds) FILTER (WHERE d.day > CURRENT_DATE - 7), 0),
		       COALESCE(SUM(d.cells_run) FILTER (WHERE d.day > CURRENT_DATE - 7), 0),
		       COALESCE(SUM(d.cells_run), 0),
		       COUNT(*) FILTER (WHERE d.active_minutes > 0)
		FROM admins a LEFT JOIN usage_daily d ON d.user_id = a.id
		WHERE a.status <> 'pending'
		GROUP BY a.id
		ORDER BY a.last_seen_at DESC NULLS LAST`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load usage per user"})
		return
	}
	var cpuSec7d []float64
	for rows.Next() {
		var u usageUser
		var cpuSec float64
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.Status, &u.AccessExpiresAt, &u.AccessHours, &u.CreatedAt, &u.LastSeenAt,
			&u.Online, &u.KernelRunning, &u.TodayActiveMin, &u.ActiveDays7d, &u.ActiveMin7d, &u.KernelMin7d, &cpuSec,
			&u.Cells7d, &u.CellsTotal, &u.ActiveDaysTotal); err != nil {
			continue
		}
		u.CPUCoreHours7d = cpuSec / 3600
		users = append(users, u)
		cpuSec7d = append(cpuSec7d, cpuSec)
	}
	rows.Close()

	var now struct {
		Online   int     `json:"online"`
		Kernels  int     `json:"kernels"`
		CPUCores float64 `json:"cpu_cores"`
		MemBytes int64   `json:"mem_bytes"`
	}
	var eng struct {
		Approved      int     `json:"approved"`        // non-superadmin accounts that were let in
		Activated     int     `json:"activated"`       // ... and ran at least one cell
		Returning     int     `json:"returning"`       // ... and were active on 2+ days
		Pending       int     `json:"pending"`         // sign-ups waiting for approval
		AvgActiveMins float64 `json:"avg_active_mins"` // per approved account, all time
	}
	var totalActive int
	for i := range users {
		u := &users[i]
		if u.Online {
			now.Online++
		}
		if u.KernelRunning {
			now.Kernels++
			if h.kernels != nil {
				ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
				if ru, err := h.kernels.Usage(ctx, u.ID); err == nil && ru.MetricsLive {
					u.CPUNow, u.MemNow = ru.CPUUsedCores, ru.MemUsedBytes
					now.CPUCores += ru.CPUUsedCores
					now.MemBytes += ru.MemUsedBytes
				}
				cancel()
			}
		}
		u.Flags = usageFlags(u, cpuSec7d[i])
		if u.Role != "superadmin" {
			eng.Approved++
			if u.CellsTotal > 0 {
				eng.Activated++
			}
			if u.ActiveDaysTotal >= 2 {
				eng.Returning++
			}
			totalActive += activeMinutesTotal(u.ID)
		}
	}
	if eng.Approved > 0 {
		eng.AvgActiveMins = float64(totalActive) / float64(eng.Approved)
	}
	_ = db.QueryRow("SELECT COUNT(*) FROM admins WHERE status = 'pending'").Scan(&eng.Pending)

	c.JSON(http.StatusOK, gin.H{"now": now, "series": series, "engagement": eng, "users": users,
		"thresholds": gin.H{"heavy_cpu_core_hours_7d": flagHeavyCPUCoreHours, "high_avg_cores": flagHighAvgCores,
			"idle_kernel_minutes_7d": flagIdleKernelMinutes, "idle_ratio": flagIdleRatio}})
}

func usageFlags(u *usageUser, cpuSec7d float64) []string {
	flags := []string{}
	if u.CPUCoreHours7d >= flagHeavyCPUCoreHours {
		flags = append(flags, "heavy_cpu")
	}
	if u.KernelMin7d >= 60 && cpuSec7d/float64(u.KernelMin7d*60) >= flagHighAvgCores {
		flags = append(flags, "high_avg_cpu")
	}
	if u.KernelMin7d >= flagIdleKernelMinutes && u.KernelMin7d > flagIdleRatio*u.ActiveMin7d {
		flags = append(flags, "idle_kernel")
	}
	return flags
}

func activeMinutesTotal(userID string) int {
	var n int
	_ = database.GetDB().QueryRow("SELECT COALESCE(SUM(active_minutes), 0) FROM usage_daily WHERE user_id = $1", userID).Scan(&n)
	return n
}
