// 本文件读取独立身份联系人列表、校验单聊参数，并独占保存本次上线实际观察到的联系人；不读取Windows客户端资料。
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"neiwangtong-gateway/internal/nativeproto"
)

// readContacts 只读加载明确的本身份列表路径；空路径表示未配置，格式、路径或I/O错误直接返回。
func readContacts(path string) (*nativeproto.Directory, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("联系人列表须为绝对路径")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return nativeproto.LoadDirectory(file)
}

// chatOptions 从联系人列表选定一条单聊请求；无聊天参数返回nil，缺目标/正文/列表或运行时未显式允许发送返回错误。
func chatOptions(userID, text string, send, execute bool, contacts *nativeproto.Directory) (*nativeproto.ChatRequest, error) {
	if !send && userID == "" && text == "" {
		return nil, nil
	}
	if execute && !send {
		return nil, fmt.Errorf("真实聊天须同时指定-run和-send；不静默忽略聊天参数")
	}
	request := &nativeproto.ChatRequest{UserID: userID, Text: text}
	if err := nativeproto.ValidateChatRequest(*request); err != nil {
		return nil, err
	}
	if contacts == nil {
		return nil, fmt.Errorf("单聊必须通过-contacts提供本独立身份的联系人列表")
	}
	if _, err := contacts.ContactByID(userID); err != nil {
		return nil, err
	}
	return request, nil
}

// saveContacts 将本次已完成就绪的对端加入现有本身份列表并写到已独占创建的新文件；返回校验、写入或Sync错误。
// 旧列表不被覆盖；它只是明确对端的资料集合，不声称发现了完整局域网联系人名册。
func saveContacts(file *os.File, ownID string, previous *nativeproto.Directory, contact nativeproto.Contact) error {
	var contacts []nativeproto.Contact
	if previous != nil {
		if previous.OwnUserID() != ownID {
			return fmt.Errorf("旧联系人列表不属于当前独立身份")
		}
		for _, item := range previous.ListContacts() {
			if item.UserID != contact.UserID {
				contacts = append(contacts, item)
			}
		}
	}
	contacts = append(contacts, contact)
	directory, err := nativeproto.NewDirectory(ownID, contacts)
	if err != nil {
		return err
	}
	return errors.Join(directory.Write(file), file.Sync())
}
