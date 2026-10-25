package web

import (
	"sync"
	"time"
)

const summaryTTL = 60 * time.Second

// orgSummary 是组织概况数据。计数字段用指针区分「未知」与 0。
type orgSummary struct {
	Licences             []licenceRow
	GlobalAdminCount     *int
	PrivilegedAdminCount *int
	TotalUsers           *int
	UpdatedAt            string
	Partial              bool
}

type licenceRow struct {
	ProductName string
	SkuPart     string
	Ratio       string
	Available   int
	StatusLabel string
	StatusBadge string
	Renewal     string
}

type summaryEntry struct {
	summary   orgSummary
	expiresAt time.Time
}

// summaryCache 按组织缓存概况。写入方（Graph 写操作完成）通过
// invalidate 递增版本号；读取期间发生失效的旧数据不会被缓存回填。
type summaryCache struct {
	mu       sync.Mutex
	entries  map[string]summaryEntry
	versions map[string]uint64
}

func newSummaryCache() *summaryCache {
	return &summaryCache{entries: map[string]summaryEntry{}, versions: map[string]uint64{}}
}

// version 返回当前版本号（读取前快照）。
func (c *summaryCache) version(orgID string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.versions[orgID]
}

// get 返回未过期的缓存概况。
func (c *summaryCache) get(orgID string) (orgSummary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[orgID]
	if !ok || time.Now().After(entry.expiresAt) {
		return orgSummary{}, false
	}
	return entry.summary, true
}

// put 在版本未变化（读取期间无写入失效）时缓存概况。
func (c *summaryCache) put(orgID string, version uint64, summary orgSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.versions[orgID] != version {
		return
	}
	c.entries[orgID] = summaryEntry{summary: summary, expiresAt: time.Now().Add(summaryTTL)}
}

// invalidate 使缓存失效；后续完成的旧读取不会回填。
func (c *summaryCache) invalidate(orgID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, orgID)
	c.versions[orgID]++
}
