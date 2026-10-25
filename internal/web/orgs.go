package web

import (
	"bytes"
	"crypto/rand"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/lcyan/office365-manager/internal/config"
)

const importMaxErrors = 20 // 导入错误提示上限，防止闪存消息撑爆 cookie

type orgRowView struct {
	ID       string
	Name     string
	ClientID string
	Status   string // untested / ok / fail
}

type orgsView struct {
	View
	Orgs []orgRowView
}

func (s *Server) handleOrgsList(w http.ResponseWriter, r *http.Request) {
	orgs := s.store.Orgs()
	view := orgsView{View: s.viewData(r, "组织管理")}
	for _, org := range orgs {
		row := orgRowView{ID: org.ID, Name: org.Name, ClientID: org.ClientID, Status: "untested"}
		if tested, ok := s.apiStatus.get(org.ID); tested {
			if ok {
				row.Status = "ok"
			} else {
				row.Status = "fail"
			}
		}
		view.Orgs = append(view.Orgs, row)
	}
	s.renderPage(w, r, http.StatusOK, "organizations", view)
}

func (s *Server) handleOrgAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	clientID := strings.TrimSpace(r.PostFormValue("client_id"))
	tenantID := strings.TrimSpace(r.PostFormValue("tenant_id"))
	secret := strings.TrimSpace(r.PostFormValue("client_secret"))
	if name == "" || clientID == "" || tenantID == "" || secret == "" {
		s.redirect(w, r, "所有字段均为必填。", "/organizations")
		return
	}
	org := config.Org{
		ID: newUUID(), Name: name,
		ClientID: clientID, TenantID: tenantID, ClientSecret: secret,
	}
	if err := s.store.AddOrg(org); err != nil {
		s.redirect(w, r, "添加组织失败："+err.Error(), "/organizations")
		return
	}
	s.log.Info("组织已添加", "org_id", org.ID)
	s.redirect(w, r, "已添加组织。", "/organizations")
}

type editOrgView struct {
	View
	Org config.Org
}

func (s *Server) handleOrgEditGET(w http.ResponseWriter, r *http.Request) {
	org, ok := s.store.Org(r.PathValue("id"))
	if !ok {
		s.redirect(w, r, "未找到该组织。", "/organizations")
		return
	}
	// 密钥不回显：表单留空表示保持原值。
	org.ClientSecret = ""
	s.renderPage(w, r, http.StatusOK, "edit_org", editOrgView{View: s.viewData(r, "编辑组织"), Org: org})
}

func (s *Server) handleOrgEditPOST(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("id")
	org, ok := s.store.Org(orgID)
	if !ok {
		s.redirect(w, r, "未找到该组织。", "/organizations")
		return
	}
	org.Name = strings.TrimSpace(r.PostFormValue("name"))
	org.ClientID = strings.TrimSpace(r.PostFormValue("client_id"))
	org.TenantID = strings.TrimSpace(r.PostFormValue("tenant_id"))
	if secret := strings.TrimSpace(r.PostFormValue("client_secret")); secret != "" {
		org.ClientSecret = secret
	}
	if org.Name == "" || org.ClientID == "" || org.TenantID == "" || org.ClientSecret == "" {
		s.redirect(w, r, "名称、客户端 ID、租户 ID 必填；密钥留空则保持不变。", "/organizations/edit/"+orgID)
		return
	}
	updated, err := s.store.ReplaceOrg(orgID, org)
	if err != nil || !updated {
		s.redirect(w, r, "保存组织失败："+errMessage(err), "/organizations/edit/"+orgID)
		return
	}
	s.graph.InvalidateOrg(orgID) // 凭据可能已更换：清除令牌与角色缓存
	s.apiStatus.drop(orgID)
	s.log.Info("组织已更新", "org_id", orgID)
	s.redirect(w, r, "组织信息已更新。", "/organizations")
}

func errMessage(err error) string {
	if err == nil {
		return "未知错误"
	}
	return err.Error()
}

type deleteOrgView struct {
	View
	Org config.Org
}

func (s *Server) handleOrgDeleteGET(w http.ResponseWriter, r *http.Request) {
	org, ok := s.store.Org(r.PathValue("id"))
	if !ok {
		s.redirect(w, r, "未找到该组织。", "/organizations")
		return
	}
	s.renderPage(w, r, http.StatusOK, "delete_org", deleteOrgView{View: s.viewData(r, "删除组织"), Org: org})
}

func (s *Server) handleOrgDeletePOST(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("id")
	deleted, err := s.store.DeleteOrg(orgID)
	if err != nil {
		s.redirect(w, r, "删除组织失败："+err.Error(), "/organizations")
		return
	}
	if !deleted {
		s.redirect(w, r, "未找到该组织。", "/organizations")
		return
	}
	s.graph.InvalidateOrg(orgID)
	s.apiStatus.drop(orgID)
	if s.activeOrgID(r) == orgID {
		s.clearActiveOrg(r)
	}
	s.log.Info("组织已删除", "org_id", orgID)
	s.redirect(w, r, "组织已删除。", "/organizations")
}

func (s *Server) handleOrgTest(w http.ResponseWriter, r *http.Request) {
	org, ok := s.store.Org(r.PathValue("id"))
	if !ok {
		s.redirect(w, r, "未找到该组织。", "/organizations")
		return
	}
	if _, err := s.graph.SubscribedSkus(r.Context(), org); err != nil {
		s.apiStatus.set(org.ID, false)
		s.log.Warn("组织 API 测试失败", "org_id", org.ID, "err", err)
		s.redirect(w, r, fmt.Sprintf("组织 %s API 测试失败：%s", org.Name, graphErrMessage(err)), "/organizations")
		return
	}
	s.apiStatus.set(org.ID, true)
	s.redirect(w, r, fmt.Sprintf("组织 %s API 测试成功。", org.Name), "/organizations")
}

func (s *Server) handleOrgTestAll(w http.ResponseWriter, r *http.Request) {
	orgs := s.store.Orgs()
	if len(orgs) == 0 {
		s.redirect(w, r, "没有已配置的组织。", "/organizations")
		return
	}
	success, failure := 0, 0
	for _, org := range orgs {
		if _, err := s.graph.SubscribedSkus(r.Context(), org); err != nil {
			s.apiStatus.set(org.ID, false)
			failure++
		} else {
			s.apiStatus.set(org.ID, true)
			success++
		}
	}
	s.redirect(w, r, fmt.Sprintf("API 测试完成：成功 %d 个，失败 %d 个。", success, failure), "/organizations")
}

func (s *Server) handleOrgTemplate(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=org_template.csv")
	_, _ = w.Write([]byte("name,client_id,tenant_id,client_secret\n"))
}

func (s *Server) handleOrgImport(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("file")
	if err != nil {
		s.redirect(w, r, "未选择文件。", "/organizations")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		s.redirect(w, r, "读取文件失败。", "/organizations")
		return
	}
	if !utf8.Valid(raw) {
		s.redirect(w, r, "导入失败：文件必须是 UTF-8 编码。", "/organizations")
		return
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // 容忍 BOM

	// 先整体验证，全部通过才写盘——不会出现半导入状态。
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		s.redirect(w, r, "导入失败：无法读取表头。", "/organizations")
		return
	}
	columns := map[string]int{}
	for i, name := range header {
		columns[strings.TrimSpace(strings.ToLower(name))] = i
	}
	for _, want := range []string{"name", "client_id", "tenant_id", "client_secret"} {
		if _, ok := columns[want]; !ok {
			s.redirect(w, r, "导入失败：缺少列 "+want+"。", "/organizations")
			return
		}
	}

	var pending []config.Org
	var problems []string
	seen := map[string]bool{} // 文件内去重
	existing := s.store.Orgs()
	for line := 1; ; line++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("第 %d 行格式错误", line+1))
			continue
		}
		field := func(column string) string {
			if idx := columns[column]; idx < len(record) {
				return strings.TrimSpace(record[idx])
			}
			return ""
		}
		org := config.Org{
			ID:           newUUID(),
			Name:         field("name"),
			ClientID:     field("client_id"),
			TenantID:     field("tenant_id"),
			ClientSecret: field("client_secret"),
		}
		switch {
		case org.Name == "" || org.ClientID == "" || org.TenantID == "" || org.ClientSecret == "":
			problems = append(problems, fmt.Sprintf("第 %d 行缺少字段", line+1))
			continue
		case seen[org.Name] || seen["cid:"+org.ClientID]:
			problems = append(problems, fmt.Sprintf("第 %d 行与文件内先前行重复", line+1))
			continue
		}
		duplicate := false
		for _, e := range existing {
			if e.Name == org.Name || e.ClientID == org.ClientID {
				duplicate = true
				break
			}
		}
		if duplicate {
			problems = append(problems, fmt.Sprintf("第 %d 行与已有组织重复（名称或客户端 ID）", line+1))
			continue
		}
		seen[org.Name] = true
		seen["cid:"+org.ClientID] = true
		pending = append(pending, org)
	}

	if len(problems) > 0 {
		shown := problems
		if len(shown) > importMaxErrors {
			shown = append(shown[:importMaxErrors], fmt.Sprintf("……共 %d 个问题", len(problems)))
		}
		s.redirect(w, r, "导入未执行（全部通过才会写入）："+strings.Join(shown, "；"), "/organizations")
		return
	}
	for _, org := range pending {
		if err := s.store.AddOrg(org); err != nil {
			s.redirect(w, r, "导入失败："+err.Error(), "/organizations")
			return
		}
	}
	s.redirect(w, r, fmt.Sprintf("成功导入 %d 个组织。", len(pending)), "/organizations")
}

func (s *Server) handleSelectOrg(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("id")
	if _, ok := s.store.Org(orgID); !ok {
		s.redirect(w, r, "未找到该组织。", "/organizations")
		return
	}
	s.setActiveOrg(r, orgID)
	target := "/users"
	if next := validNext(r.URL.Query().Get("next")); next != "" {
		target = next
	}
	s.redirect(w, r, "已切换到选定组织。", target)
}

// graphErrMessage 把 Graph 错误转为适合闪存的短消息（不含敏感细节）。
func graphErrMessage(err error) string {
	return errMessage(err)
}

// newUUID 生成 UUID v4（crypto/rand）。
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand 不可用：" + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexDigits[v>>4], hexDigits[v&0x0f])
	}
	return string(out)
}
