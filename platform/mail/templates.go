package mail

import (
	"embed"
	"fmt"
	"html/template"
	"reflect"
	txt "text/template"
)

//go:embed templates
var templatesFS embed.FS

// spec описывает один шаблон: тип данных, тему и прехедер. Темы и прехедеры
// повторяют мета-блоки прототипов docs/design/prototypes/Email*.dc.html; поля
// письма доступны в них напрямую ({{ .Field }}).
type spec struct {
	dataType  reflect.Type
	subject   string
	preheader string
}

var specs = map[Template]spec{
	TemplateVerify: {
		dataType:  reflect.TypeOf(VerifyData{}),
		subject:   "Подтвердите почту в MarketMesh",
		preheader: "Один шаг — и аккаунт готов к работе.",
	},
	TemplateCode: {
		dataType:  reflect.TypeOf(CodeData{}),
		subject:   "Код для входа в MarketMesh",
		preheader: "Код действует {{ .CodeTTL }} и подходит для одного входа.",
	},
	TemplateNewLogin: {
		dataType:  reflect.TypeOf(NewLoginData{}),
		subject:   "Вход в аккаунт с нового устройства",
		preheader: "Если это вы — ничего делать не нужно.",
	},
	TemplatePasswordReset: {
		dataType:  reflect.TypeOf(PasswordResetData{}),
		subject:   "Сброс пароля в MarketMesh",
		preheader: "Ссылка действует {{ .LinkTTL }}.",
	},
	TemplatePasswordChanged: {
		dataType:  reflect.TypeOf(PasswordChangedData{}),
		subject:   "Пароль от аккаунта изменён",
		preheader: "Если это не вы — восстановите доступ сейчас.",
	},
	TemplateEmailChangeAlert: {
		dataType:  reflect.TypeOf(EmailChangeAlertData{}),
		subject:   "Запрошена смена адреса почты",
		preheader: "Если это не вы — отмените смену по ссылке.",
	},
	TemplateEmailChangeConfirm: {
		dataType:  reflect.TypeOf(EmailChangeConfirmData{}),
		subject:   "Подтвердите новый адрес почты",
		preheader: "Пока адрес не подтверждён, вход остаётся по старому.",
	},
	TemplateAccountLocked: {
		dataType:  reflect.TypeOf(AccountLockedData{}),
		subject:   "Вход в аккаунт временно заблокирован",
		preheader: "Мы остановили подбор пароля. Аккаунт цел.",
	},
	TemplateSessionsClosed: {
		dataType:  reflect.TypeOf(SessionsClosedData{}),
		subject:   "Мы закрыли сессии на всех устройствах",
		preheader: "Чтобы продолжить, войдите заново.",
	},
	TemplateTwoFactorOn: {
		dataType:  reflect.TypeOf(TwoFactorOnData{}),
		subject:   "Подтверждение входа включено",
		preheader: "Теперь при входе понадобится код из письма.",
	},
	TemplateTwoFactorOff: {
		dataType:  reflect.TypeOf(TwoFactorOffData{}),
		subject:   "Подтверждение входа отключено",
		preheader: "Если это не вы — включите его обратно.",
	},
	TemplateBackupCodes: {
		dataType:  reflect.TypeOf(BackupCodesData{}),
		subject:   "Резервные коды обновлены",
		preheader: "Старые коды больше не работают.",
	},
	TemplateSellerDecision: {
		dataType:  reflect.TypeOf(SellerDecisionData{}),
		subject:   "{{ if .Approved }}Магазин подключён к MarketMesh{{ else }}Заявка на подключение магазина отклонена{{ end }}",
		preheader: "{{ if .Approved }}Портал открыт — можно заводить товары.{{ else }}Рассказываем, что поправить и как подать заявку снова.{{ end }}",
	},
	TemplateStaffInvite: {
		dataType:  reflect.TypeOf(StaffInviteData{}),
		subject:   "Доступ к внутреннему порталу MarketMesh",
		preheader: "Вход через рабочий аккаунт, пароль заводить не нужно.",
	},
	TemplateAccountDeleted: {
		dataType:  reflect.TypeOf(AccountDeletedData{}),
		subject:   "Аккаунт будет удалён {{ .DeletionDate }}",
		preheader: "До этой даты удаление можно отменить.",
	},
}

// parseEntries компилирует все шаблоны. Значения бренда доступны в шаблонах
// через функции domain, supportEmail, sellersSupportEmail и itSupportEmail.
func parseEntries(brand Brand) (map[Template]*entry, error) {
	funcs := map[string]any{
		"domain":              func() string { return brand.Domain },
		"supportEmail":        func() string { return brand.SupportEmail },
		"sellersSupportEmail": func() string { return brand.SellersSupportEmail },
		"itSupportEmail":      func() string { return brand.ITSupportEmail },
	}
	entries := make(map[Template]*entry, len(specs))
	for name, s := range specs {
		subject, err := txt.New("subject").Funcs(funcs).Parse(s.subject)
		if err != nil {
			return nil, fmt.Errorf("mail: тема %q: %w", name, err)
		}
		preheader, err := txt.New("preheader").Funcs(funcs).Parse(s.preheader)
		if err != nil {
			return nil, fmt.Errorf("mail: прехедер %q: %w", name, err)
		}
		html, err := template.New("base").Funcs(funcs).
			ParseFS(templatesFS, "templates/base.html", "templates/"+string(name)+".html")
		if err != nil {
			return nil, fmt.Errorf("mail: html %q: %w", name, err)
		}
		text, err := txt.New(string(name)).Funcs(funcs).
			ParseFS(templatesFS, "templates/"+string(name)+".txt")
		if err != nil {
			return nil, fmt.Errorf("mail: text %q: %w", name, err)
		}
		entries[name] = &entry{
			dataType:  s.dataType,
			subject:   subject,
			preheader: preheader,
			html:      html,
			text:      text,
		}
	}
	return entries, nil
}
