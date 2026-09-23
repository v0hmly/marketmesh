package mail

import "errors"

// Наборы данных для шаблонов. Все строковые поля обязательны, если не сказано
// иное; сроки (TTL) и даты приходят уже отформатированными по-русски
// («10 минут», «21 сентября 2026, 14:03, UTC+3») — пакет не занимается
// форматированием времени и плюрализацией.

// VerifyData — подтверждение почты при регистрации.
type VerifyData struct {
	// RecipientEmail — почта, которую подтверждаем.
	RecipientEmail string
	// ConfirmURL — ссылка подтверждения.
	ConfirmURL string
	// LinkTTL — срок действия ссылки.
	LinkTTL string
	// Deadline — необязательные местная дата, время и явное UTC-смещение.
	Deadline string
}

func (d VerifyData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.ConfirmURL == "":
		return errors.New("ссылка подтверждения пуста")
	case d.LinkTTL == "":
		return errors.New("срок действия ссылки пуст")
	}
	return nil
}

// CodeData — код для подтверждения входа.
type CodeData struct {
	// RecipientEmail — почта, для которой подтверждается вход.
	RecipientEmail string
	// Code — шестизначный код.
	Code string
	// CodeTTL — срок действия кода.
	CodeTTL string
	// Deadline — необязательные местная дата, время и явное UTC-смещение.
	Deadline string
	// PasswordResetURL — ссылка на смену пароля из блока «это не вы».
	PasswordResetURL string
}

func (d CodeData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case len(d.Code) != 6:
		return errors.New("код должен состоять из шести цифр")
	case d.CodeTTL == "":
		return errors.New("срок действия кода пуст")
	case d.PasswordResetURL == "":
		return errors.New("ссылка на смену пароля пуста")
	}
	for _, r := range d.Code {
		if r < '0' || r > '9' {
			return errors.New("код должен состоять из шести цифр")
		}
	}
	return nil
}

// NewLoginData — вход с нового устройства.
type NewLoginData struct {
	RecipientEmail string
	// Timestamp — дата, время и часовой пояс входа.
	Timestamp string
	// Device — устройство и система, например «MacBook, macOS».
	Device string
	// Browser — браузер, например «Chrome 140».
	Browser string
	// IP — адрес, с которого вошли.
	IP string
	// Location — примерные город и страна по IP.
	Location string
	// PasswordResetURL — ссылка защитного действия «Это не я — сменить пароль».
	PasswordResetURL string
}

func (d NewLoginData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.Timestamp == "":
		return errors.New("время входа пусто")
	case d.Device == "":
		return errors.New("устройство пусто")
	case d.Browser == "":
		return errors.New("браузер пуст")
	case d.IP == "":
		return errors.New("IP-адрес пуст")
	case d.Location == "":
		return errors.New("локация пуста")
	case d.PasswordResetURL == "":
		return errors.New("ссылка на смену пароля пуста")
	}
	return nil
}

// PasswordResetData — ссылка на сброс пароля.
type PasswordResetData struct {
	RecipientEmail string
	// ResetURL — одноразовая ссылка сброса.
	ResetURL string
	// LinkTTL — срок действия ссылки.
	LinkTTL string
	// Deadline — необязательные местная дата, время и явное UTC-смещение.
	Deadline string
}

func (d PasswordResetData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.ResetURL == "":
		return errors.New("ссылка сброса пуста")
	case d.LinkTTL == "":
		return errors.New("срок действия ссылки пуст")
	}
	return nil
}

// PasswordChangedData — уведомление об изменении пароля.
type PasswordChangedData struct {
	RecipientEmail string
	// Timestamp — когда изменили, с часовым поясом.
	Timestamp string
	// DeviceBrowser — устройство и браузер, откуда меняли.
	DeviceBrowser string
	IP            string
	// RecoveryURL — ссылка «Восстановить доступ».
	RecoveryURL string
}

func (d PasswordChangedData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.Timestamp == "":
		return errors.New("время изменения пусто")
	case d.DeviceBrowser == "":
		return errors.New("устройство и браузер пусты")
	case d.IP == "":
		return errors.New("IP-адрес пуст")
	case d.RecoveryURL == "":
		return errors.New("ссылка восстановления пуста")
	}
	return nil
}

// EmailChangeAlertData — запрос смены почты, письмо на старый адрес.
type EmailChangeAlertData struct {
	// OldEmail — текущий адрес аккаунта.
	OldEmail string
	// NewEmail — адрес, на который просят перенести аккаунт.
	NewEmail  string
	Timestamp string
	IP        string
	// CancelURL — ссылка отмены смены адреса.
	CancelURL string
	// CancelTTL — срок действия ссылки отмены.
	CancelTTL string
	// Deadline — необязательные местная дата, время и явное UTC-смещение.
	Deadline string
}

func (d EmailChangeAlertData) validate() error {
	switch {
	case d.OldEmail == "":
		return errors.New("старая почта пуста")
	case d.NewEmail == "":
		return errors.New("новая почта пуста")
	case d.Timestamp == "":
		return errors.New("время запроса пусто")
	case d.IP == "":
		return errors.New("IP-адрес пуст")
	case d.CancelURL == "":
		return errors.New("ссылка отмены пуста")
	case d.CancelTTL == "":
		return errors.New("срок отмены пуст")
	}
	return nil
}

// EmailChangeConfirmData — подтверждение нового адреса, письмо на новый адрес.
type EmailChangeConfirmData struct {
	OldEmail string
	NewEmail string
	// ConfirmURL — ссылка подтверждения нового адреса.
	ConfirmURL string
	// LinkTTL — срок действия ссылки.
	LinkTTL string
	// Deadline — необязательные местная дата, время и явное UTC-смещение.
	Deadline string
}

func (d EmailChangeConfirmData) validate() error {
	switch {
	case d.OldEmail == "":
		return errors.New("старая почта пуста")
	case d.NewEmail == "":
		return errors.New("новая почта пуста")
	case d.ConfirmURL == "":
		return errors.New("ссылка подтверждения пуста")
	case d.LinkTTL == "":
		return errors.New("срок действия ссылки пуст")
	}
	return nil
}

// AccountLockedData — временная блокировка входа после подбора пароля.
type AccountLockedData struct {
	RecipientEmail string
	// LockDuration — срок блокировки.
	LockDuration string
	Timestamp    string
	// Attempts — число неудачных попыток, после которого закрыли вход.
	Attempts int
	IP       string
	Location string
	// PasswordResetURL — ссылка на смену пароля.
	PasswordResetURL string
}

func (d AccountLockedData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.LockDuration == "":
		return errors.New("срок блокировки пуст")
	case d.Timestamp == "":
		return errors.New("время блокировки пусто")
	case d.Attempts < 1:
		return errors.New("число попыток должно быть положительным")
	case d.IP == "":
		return errors.New("IP-адрес пуст")
	case d.Location == "":
		return errors.New("локация пуста")
	case d.PasswordResetURL == "":
		return errors.New("ссылка на смену пароля пуста")
	}
	return nil
}

// SessionsClosedData — уведомление о закрытии всех сессий.
type SessionsClosedData struct {
	RecipientEmail string
	// Timestamp — когда сессии закрыли, с часовым поясом.
	Timestamp string
	// LoginURL — ссылка «Войти снова».
	LoginURL string
}

func (d SessionsClosedData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.Timestamp == "":
		return errors.New("время закрытия сессий пусто")
	case d.LoginURL == "":
		return errors.New("ссылка входа пуста")
	}
	return nil
}

// TwoFactorOnData — подтверждение входа кодом включено.
type TwoFactorOnData struct {
	RecipientEmail string
	// BackupCodesURL — ссылка «Открыть резервные коды» в аккаунте.
	BackupCodesURL string
	// SecurityURL is used when recovery codes are not offered by the application.
	SecurityURL string
}

func (d TwoFactorOnData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.BackupCodesURL == "" && d.SecurityURL == "":
		return errors.New("ссылка на настройки безопасности пуста")
	}
	return nil
}

// TwoFactorOffData — подтверждение входа кодом отключено.
type TwoFactorOffData struct {
	RecipientEmail string
	Timestamp      string
	// DeviceBrowser — устройство и браузер, откуда отключили.
	DeviceBrowser string
	IP            string
	// EnableURL — ссылка «Включить подтверждение» обратно.
	EnableURL string
}

func (d TwoFactorOffData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.Timestamp == "":
		return errors.New("время отключения пусто")
	case d.DeviceBrowser == "":
		return errors.New("устройство и браузер пусты")
	case d.IP == "":
		return errors.New("IP-адрес пуст")
	case d.EnableURL == "":
		return errors.New("ссылка включения пуста")
	}
	return nil
}

// BackupCodesData — обновление набора резервных кодов. Сами коды письмом не
// отправляются: письмо ведёт за ними в аккаунт.
type BackupCodesData struct {
	RecipientEmail string
	// CodesCount — число кодов в новом наборе.
	CodesCount int
	// BackupCodesURL — ссылка «Открыть резервные коды» в аккаунте.
	BackupCodesURL string
}

func (d BackupCodesData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.CodesCount < 1:
		return errors.New("число кодов должно быть положительным")
	case d.BackupCodesURL == "":
		return errors.New("ссылка на резервные коды пуста")
	}
	return nil
}

// SellerDecisionData — решение по заявке продавца. При Approved поля
// RejectReason и FixInstructions не используются; при отказе обязательны.
type SellerDecisionData struct {
	// ShopName — название магазина из заявки.
	ShopName string
	// Approved — true, если заявка одобрена.
	Approved bool
	// PortalURL — ссылка «Войти в портал» (одобрение) или «Подать заявку снова» (отказ).
	PortalURL string
	// GuideURL — ссылка на инструкцию для продавца; обязательна при одобрении.
	GuideURL string
	// RejectReason — причина отказа; обязательна при отказе.
	RejectReason string
	// FixInstructions — что исправить в заявке; обязательно при отказе.
	FixInstructions string
}

func (d SellerDecisionData) validate() error {
	switch {
	case d.ShopName == "":
		return errors.New("название магазина пусто")
	case d.PortalURL == "":
		return errors.New("ссылка на портал пуста")
	}
	if d.Approved {
		if d.GuideURL == "" {
			return errors.New("ссылка на инструкцию пуста")
		}
		return nil
	}
	switch {
	case d.RejectReason == "":
		return errors.New("причина отказа пуста")
	case d.FixInstructions == "":
		return errors.New("инструкции по исправлению пусты")
	}
	return nil
}

// StaffInviteData — приглашение сотрудника во внутренний портал.
type StaffInviteData struct {
	// InviterName — кто пригласил, например «Анна Соколова».
	InviterName string
	// Role — выданная роль, например «модератор».
	Role string
	// InviteURL — ссылка «Принять приглашение».
	InviteURL string
	// InviteTTL — срок действия приглашения.
	InviteTTL string
}

func (d StaffInviteData) validate() error {
	switch {
	case d.InviterName == "":
		return errors.New("имя пригласившего пусто")
	case d.Role == "":
		return errors.New("роль пуста")
	case d.InviteURL == "":
		return errors.New("ссылка приглашения пуста")
	case d.InviteTTL == "":
		return errors.New("срок действия приглашения пуст")
	}
	return nil
}

// AccountDeletedData — подтверждение запроса на удаление аккаунта с отсрочкой.
type AccountDeletedData struct {
	RecipientEmail string
	// DeletionDate — дата окончательного удаления; до неё удаление можно отменить.
	DeletionDate string
	// CancelURL — ссылка «Отменить удаление».
	CancelURL string
}

func (d AccountDeletedData) validate() error {
	switch {
	case d.RecipientEmail == "":
		return errors.New("почта получателя пуста")
	case d.DeletionDate == "":
		return errors.New("дата удаления пуста")
	case d.CancelURL == "":
		return errors.New("ссылка отмены удаления пуста")
	}
	return nil
}
