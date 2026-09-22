package mail

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"reflect"
	txt "text/template"
)

// Template именует один из пятнадцати шаблонов писем авторизации.
// Строковое значение совпадает с именем файла в templates без расширения.
type Template string

const (
	TemplateVerify             Template = "verify"
	TemplateCode               Template = "code"
	TemplateNewLogin           Template = "new_login"
	TemplatePasswordReset      Template = "password_reset"
	TemplatePasswordChanged    Template = "password_changed"
	TemplateEmailChangeAlert   Template = "email_change_alert"
	TemplateEmailChangeConfirm Template = "email_change_confirm"
	TemplateAccountLocked      Template = "account_locked"
	TemplateSessionsClosed     Template = "sessions_closed"
	TemplateTwoFactorOn        Template = "two_factor_on"
	TemplateTwoFactorOff       Template = "two_factor_off"
	TemplateBackupCodes        Template = "backup_codes"
	TemplateSellerDecision     Template = "seller_decision"
	TemplateStaffInvite        Template = "staff_invite"
	TemplateAccountDeleted     Template = "account_deleted"
)

// Templates возвращает все известные шаблоны в стабильном порядке.
func Templates() []Template {
	return []Template{
		TemplateVerify,
		TemplateCode,
		TemplateNewLogin,
		TemplatePasswordReset,
		TemplatePasswordChanged,
		TemplateEmailChangeAlert,
		TemplateEmailChangeConfirm,
		TemplateAccountLocked,
		TemplateSessionsClosed,
		TemplateTwoFactorOn,
		TemplateTwoFactorOff,
		TemplateBackupCodes,
		TemplateSellerDecision,
		TemplateStaffInvite,
		TemplateAccountDeleted,
	}
}

// Brand — общие для всех писем значения, доступные в шаблонах через функции
// domain, supportEmail, sellersSupportEmail и itSupportEmail.
type Brand struct {
	// Domain — домен отправителя; From строится как MarketMesh <noreply@Domain>.
	Domain string
	// SupportEmail — почта поддержки покупателей.
	SupportEmail string
	// SellersSupportEmail — почта поддержки продавцов.
	SellersSupportEmail string
	// ITSupportEmail — почта ИТ-поддержки для писем сотрудникам.
	ITSupportEmail string
}

func (b Brand) validate() error {
	switch {
	case b.Domain == "":
		return errors.New("mail: домен отправителя пуст")
	case b.SupportEmail == "":
		return errors.New("mail: почта поддержки пуста")
	case b.SellersSupportEmail == "":
		return errors.New("mail: почта поддержки продавцов пуста")
	case b.ITSupportEmail == "":
		return errors.New("mail: почта ИТ-поддержки пуста")
	}
	return nil
}

// Message — отрендеренное письмо: тема, прехедер, HTML и plaintext части.
type Message struct {
	// From — адрес отправителя вида "MarketMesh <noreply@домен>".
	From string
	// Subject — тема письма.
	Subject string
	// Preheader — текст превью, уже встроен в HTML скрытым блоком.
	Preheader string
	// HTML — табличная вёрстка с инлайн-стилями.
	HTML []byte
	// Text — plaintext-часть для клиентов без HTML.
	Text []byte
}

// Renderer рендерит шаблоны с общими значениями Brand. Готов через NewRenderer;
// безопасен для конкурентного использования.
type Renderer struct {
	brand   Brand
	entries map[Template]*entry
}

// NewRenderer парсит все встроенные шаблоны и проверяет Brand.
func NewRenderer(brand Brand) (*Renderer, error) {
	if err := brand.validate(); err != nil {
		return nil, err
	}
	entries, err := parseEntries(brand)
	if err != nil {
		return nil, err
	}
	return &Renderer{brand: brand, entries: entries}, nil
}

// view — данные, которые видят шаблоны: поля письма через .Data,
// отрендеренный прехедер через .Preheader, общие значения бренда — через
// функции domain, supportEmail, sellersSupportEmail и itSupportEmail.
type view struct {
	Preheader string
	Data      any
}

// Render собирает письмо по шаблону tpl из типизированных данных data.
// Тип data обязан совпадать с data-структурой шаблона (например, CodeData для
// TemplateCode) и пройти её валидацию; иначе возвращается ошибка.
func (r *Renderer) Render(tpl Template, data any) (Message, error) {
	entry, ok := r.entries[tpl]
	if !ok {
		return Message{}, fmt.Errorf("mail: неизвестный шаблон %q", tpl)
	}
	if data == nil || reflect.TypeOf(data) != entry.dataType {
		return Message{}, fmt.Errorf(
			"mail: шаблон %q требует данные типа %s", tpl, entry.dataType.Name())
	}
	if v, ok := data.(interface{ validate() error }); ok {
		if err := v.validate(); err != nil {
			return Message{}, fmt.Errorf("mail: данные для %q: %w", tpl, err)
		}
	}
	var preheader bytes.Buffer
	if err := entry.preheader.Execute(&preheader, data); err != nil {
		return Message{}, fmt.Errorf("mail: прехедер %q: %w", tpl, err)
	}
	v := view{Preheader: preheader.String(), Data: data}
	var subject bytes.Buffer
	if err := entry.subject.Execute(&subject, data); err != nil {
		return Message{}, fmt.Errorf("mail: тема %q: %w", tpl, err)
	}
	var html bytes.Buffer
	if err := entry.html.ExecuteTemplate(&html, "base.html", v); err != nil {
		return Message{}, fmt.Errorf("mail: html %q: %w", tpl, err)
	}
	var text bytes.Buffer
	if err := entry.text.ExecuteTemplate(&text, string(tpl)+".txt", v); err != nil {
		return Message{}, fmt.Errorf("mail: text %q: %w", tpl, err)
	}
	return Message{
		From:      fmt.Sprintf("MarketMesh <noreply@%s>", r.brand.Domain),
		Subject:   subject.String(),
		Preheader: v.Preheader,
		HTML:      html.Bytes(),
		Text:      text.Bytes(),
	}, nil
}

// entry — скомпилированные части одного письма.
type entry struct {
	dataType  reflect.Type
	subject   *txt.Template
	preheader *txt.Template
	html      *template.Template
	text      *txt.Template
}
