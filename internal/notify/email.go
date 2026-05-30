package notify

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strconv"
	"strings"
)

type EmailNotifier struct {
	Host string
	Port string
	User string
	Pass string
	From string
	To   string
}

func (e *EmailNotifier) Name() string {
	return "Email"
}

func (e *EmailNotifier) Send(title, content string) error {
	port, _ := strconv.Atoi(e.Port)
	if port == 0 {
		port = 465
	}

	if title == "" {
		title = "Float 监控系统通知"
	}

	subject := fmt.Sprintf("Subject: =?UTF-8?B?%s?=\r\n", title) // 简单编码处理中文标题
	contentType := "Content-Type: text/plain; charset=UTF-8\r\n\r\n"
	
	cleanText := strings.ReplaceAll(content, "<b>", "")
	cleanText = strings.ReplaceAll(cleanText, "</b>", "")
	msg := []byte(subject + contentType + cleanText)

	auth := smtp.PlainAuth("", e.User, e.Pass, e.Host)

	if port == 465 {
		tlsconfig := &tls.Config{InsecureSkipVerify: true, ServerName: e.Host}
		conn, err := tls.Dial("tcp", fmt.Sprintf("%s:%d", e.Host, port), tlsconfig)
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, e.Host)
		if err != nil {
			return err
		}
		if err = c.Auth(auth); err != nil {
			return err
		}
		if err = c.Mail(e.From); err != nil {
			return err
		}
		if err = c.Rcpt(e.To); err != nil {
			return err
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		_, err = w.Write(msg)
		if err != nil {
			return err
		}
		err = w.Close()
		if err != nil {
			return err
		}
		return c.Quit()
	}

	return smtp.SendMail(fmt.Sprintf("%s:%d", e.Host, port), auth, e.From, []string{e.To}, msg)
}