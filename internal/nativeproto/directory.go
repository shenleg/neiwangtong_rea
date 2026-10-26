// 本文件维护独立身份自己的联系人列表，并按原始UserID选择目标；不读取Windows客户端缓存或推测网络端点。
package nativeproto

import (
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
)

// Contact 是独立节点目录中的联系人；Discovery 是明确的发现端点，不由名称或UserID推算。
type Contact struct {
	UserID    string         `json:"user_id"`
	Name      string         `json:"name"`
	Discovery netip.AddrPort `json:"discovery"`
}

// Directory 是本身份的联系人快照；构造后只读使用，不扫描端点、不读取原客户端数据库。
type Directory struct {
	ownID    string
	contacts []Contact
}

type directoryDocument struct {
	OwnUserID string    `json:"own_user_id"`
	Contacts  []Contact `json:"contacts"`
}

// NewDirectory 校验并复制独立身份及其联系人列表，返回只读目录。
// 身份无效、联系人重复、目标端点非法或名称无效时返回错误；列表条目不证明联系人当前在线。
func NewDirectory(ownUserID string, contacts []Contact) (*Directory, error) {
	if err := validateMessageID(ownUserID); err != nil {
		return nil, err
	}
	if len(ownUserID) > 64 {
		return nil, fmt.Errorf("本机身份超出节点64字节槽预算")
	}
	directory := &Directory{ownID: ownUserID, contacts: append([]Contact(nil), contacts...)}
	seen := make(map[string]bool, len(contacts))
	for _, contact := range contacts {
		if err := validateMessageID(contact.UserID); err != nil {
			return nil, err
		}
		if contact.UserID == ownUserID || seen[contact.UserID] || len(contact.UserID) > 64 {
			return nil, fmt.Errorf("联系人身份重复、超长或等于本机")
		}
		if err := validateMessageID(contact.Name); err != nil {
			return nil, fmt.Errorf("联系人名称: %w", err)
		}
		endpoint := contact.Discovery
		if !endpoint.IsValid() || !endpoint.Addr().Is4() || endpoint.Port() == 0 || endpoint.Addr().IsUnspecified() || endpoint.Addr().IsMulticast() || endpoint.Addr() == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			return nil, fmt.Errorf("联系人必须有明确的IPv4单播发现端点")
		}
		seen[contact.UserID] = true
	}
	return directory, nil
}

// LoadDirectory 从reader读取一份本身份联系人JSON，返回通过校验的目录。
// 未知字段、尾随数据、JSON格式或联系人契约错误时返回错误；不读取其他文件或访问网络。
func LoadDirectory(reader io.Reader) (*Directory, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var document directoryDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("读取联系人列表: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("联系人列表后存在多余数据: %v", err)
	}
	return NewDirectory(document.OwnUserID, document.Contacts)
}

// Write 将本身份及联系人列表写入writer；返回编码或写入错误，不打开或覆盖文件。
func (d *Directory) Write(writer io.Writer) error {
	return json.NewEncoder(writer).Encode(directoryDocument{OwnUserID: d.ownID, Contacts: d.contacts})
}

// OwnUserID 返回这份列表所属的独立身份；无外部I/O或错误。
func (d *Directory) OwnUserID() string { return d.ownID }

// ListContacts 返回联系人列表的副本；不探测在线状态，无外部I/O或错误。
func (d *Directory) ListContacts() []Contact { return append([]Contact(nil), d.contacts...) }

// ContactByID 按列表中的原始UserID选定唯一联系人；未命中返回错误，不按名称、IP或顺序替换目标。
func (d *Directory) ContactByID(userID string) (Contact, error) {
	for _, contact := range d.contacts {
		if contact.UserID == userID {
			return contact, nil
		}
	}
	return Contact{}, fmt.Errorf("指定联系人不在当前身份的联系人列表中")
}
