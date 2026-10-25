package web

import (
	"sync"

	"github.com/lcyan/office365-manager/internal/config"
)

// orgStatus 记录每个组织最近一次 API 连通性测试结果（进程内，不落盘）。
type orgStatus struct {
	mu     sync.RWMutex
	status map[string]bool // org ID → 最近一次测试是否成功
}

func newOrgStatus() *orgStatus {
	return &orgStatus{status: map[string]bool{}}
}

func (o *orgStatus) set(orgID string, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status[orgID] = ok
}

// get 返回 (是否测试过, 是否成功)。
func (o *orgStatus) get(orgID string) (tested, ok bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	ok, tested = o.status[orgID]
	return tested, ok
}

func (o *orgStatus) drop(orgID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.status, orgID)
}

// counts 统计给定组织列表中的可用/不可用数量。
func (o *orgStatus) counts(orgs []config.Org) (available, unavailable int) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, org := range orgs {
		if ok, tested := o.status[org.ID]; tested {
			if ok {
				available++
			} else {
				unavailable++
			}
		}
	}
	return available, unavailable
}
