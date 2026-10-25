// Package config 加载并持久化应用配置文件（组织凭据与登录密码哈希）。
//
// 配置以 JSON 保存在磁盘上，所有写入都是原子操作（同目录临时文件 + rename），
// 进程在写入中途崩溃不会破坏原文件。Store 的所有方法并发安全。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Version 是当前配置文件格式版本。旧版（无 version 字段）文件可读入，
// 首次保存时自动升级到该版本。
const Version = 2

// Org 保存单个 Microsoft Entra 租户的应用注册凭据。
type Org struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ClientID     string `json:"client_id"`
	TenantID     string `json:"tenant_id"`
	ClientSecret string `json:"client_secret"`
}

// fileData 与磁盘 JSON 文档一一对应。Version 用指针区分「旧版无 version 字段」
// 与「version: 2」。
type fileData struct {
	Version       *int   `json:"version,omitempty"`
	PasswordHash  string `json:"password_hash"`
	Organizations []Org  `json:"organizations"`
}

// Store 持有内存中的配置并负责原子落盘。
type Store struct {
	mu   sync.RWMutex
	path string
	data fileData
}

// Load 读取配置文件；文件不存在时用 defaultPasswordHash 初始化默认配置
// （空组织列表）并落盘。JSON 无效或结构不完整时返回错误，原文件保持不动。
func Load(path, defaultPasswordHash string) (*Store, error) {
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s := &Store{path: path, data: fileData{
			Version:       versionPtr(Version),
			PasswordHash:  defaultPasswordHash,
			Organizations: []Org{},
		}}
		if err := s.Save(); err != nil {
			return nil, fmt.Errorf("初始化配置文件失败：%w", err)
		}
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("读取配置文件失败：%w", err)
	}

	var data fileData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, errors.New("配置文件为空或 JSON 无效，请从已验证的备份恢复；原文件未修改。")
	}
	if data.Version != nil && *data.Version > Version {
		return nil, fmt.Errorf("配置文件版本 %d 高于本程序支持的版本 %d，请升级程序。", *data.Version, Version)
	}
	if err := validate(data); err != nil {
		return nil, fmt.Errorf("配置文件结构无效（%s），需要 password_hash 和完整的 organizations 列表；原文件未修改。", err)
	}
	if data.Organizations == nil {
		data.Organizations = []Org{}
	}
	return &Store{path: path, data: data}, nil
}

func validate(data fileData) error {
	if data.PasswordHash == "" {
		return errors.New("缺少 password_hash")
	}
	for i, org := range data.Organizations {
		if org.ID == "" || org.Name == "" || org.ClientID == "" ||
			org.TenantID == "" || org.ClientSecret == "" {
			return fmt.Errorf("第 %d 个组织缺少 id/name/client_id/tenant_id/client_secret", i+1)
		}
	}
	return nil
}

// Path 返回配置文件路径。
func (s *Store) Path() string { return s.path }

// Orgs 返回组织列表的副本，调用方可安全修改。
func (s *Store) Orgs() []Org {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Org, len(s.data.Organizations))
	copy(out, s.data.Organizations)
	return out
}

// Org 按 ID 查找组织。
func (s *Store) Org(id string) (Org, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, org := range s.data.Organizations {
		if org.ID == id {
			return org, true
		}
	}
	return Org{}, false
}

// AddOrg 追加组织并保存。要求字段完整，名称与 client_id 不得与现有组织重复。
func (s *Store) AddOrg(org Org) error {
	if err := validateOrg(org); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.data.Organizations {
		if existing.Name == org.Name {
			return fmt.Errorf("组织名称 %q 已存在", org.Name)
		}
		if existing.ClientID == org.ClientID {
			return fmt.Errorf("客户端 ID %q 已存在", org.ClientID)
		}
	}
	s.data.Organizations = append(s.data.Organizations, org)
	return s.saveLocked()
}

// ReplaceOrg 以新内容替换 id 对应的组织；id 不变，找不到时返回 false。
func (s *Store) ReplaceOrg(id string, org Org) (bool, error) {
	if err := validateOrg(org); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.data.Organizations {
		if existing.ID == id {
			org.ID = id
			s.data.Organizations[i] = org
			return true, s.saveLocked()
		}
	}
	return false, nil
}

// DeleteOrg 删除组织；组织不存在时返回 false。
func (s *Store) DeleteOrg(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.data.Organizations[:0]
	found := false
	for _, org := range s.data.Organizations {
		if org.ID == id {
			found = true
			continue
		}
		kept = append(kept, org)
	}
	if !found {
		return false, nil
	}
	s.data.Organizations = kept
	return true, s.saveLocked()
}

// PasswordHash 返回当前登录密码哈希。
func (s *Store) PasswordHash() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.PasswordHash
}

// SetPasswordHash 更新登录密码哈希并保存。
func (s *Store) SetPasswordHash(hash string) error {
	if hash == "" {
		return errors.New("密码哈希不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.PasswordHash = hash
	return s.saveLocked()
}

// Save 将当前配置原子写入磁盘。写入流程：同目录临时文件 → fsync → rename，
// 任一时刻磁盘上要么是旧文件、要么是完整的新文件。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	data := s.data
	data.Version = versionPtr(Version)
	if data.Organizations == nil {
		data.Organizations = []Org{}
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败：%w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败：%w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败：%w", err)
	}
	tmpName := tmp.Name()
	// 成功 rename 后 Remove 是 no-op；失败路径上确保不遗留临时文件。
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("写入临时文件失败：%w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("设置文件权限失败：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("同步临时文件失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败：%w", err)
	}
	// Windows 上杀软/索引器可能短暂锁定目标文件导致 rename 失败，
	// 短重试后仍失败才放弃（Linux 上首次即成功）。
	if err := renameWithRetry(tmpName, s.path); err != nil {
		_ = removeWithRetry(tmpName)
		return fmt.Errorf("替换配置文件失败：%w", err)
	}
	return nil
}

func renameWithRetry(oldPath, newPath string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.Rename(oldPath, newPath); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

func removeWithRetry(path string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.Remove(path); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

func validateOrg(org Org) error {
	var missing []string
	for _, field := range []struct{ name, value string }{
		{"id", org.ID}, {"name", org.Name}, {"client_id", org.ClientID},
		{"tenant_id", org.TenantID}, {"client_secret", org.ClientSecret},
	} {
		if field.value == "" {
			missing = append(missing, field.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("组织缺少必填字段：%v", missing)
	}
	return nil
}

func versionPtr(v int) *int { return &v }
