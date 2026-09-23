package mail

import (
	"bytes"
	"strings"
	"testing"
)

func testBrand() Brand {
	return Brand{
		Domain:              "marketmesh.test",
		SupportEmail:        "help@marketmesh.test",
		SellersSupportEmail: "sellers@marketmesh.test",
		ITSupportEmail:      "it@marketmesh.test",
	}
}

func fixtures() map[Template]any {
	return map[Template]any{
		TemplateVerify: VerifyData{
			RecipientEmail: "anna@example.ru",
			ConfirmURL:     "https://marketmesh.test/confirm?t=abc",
			LinkTTL:        "24 часа",
		},
		TemplateCode: CodeData{
			RecipientEmail:   "anna@example.ru",
			Code:             "482913",
			CodeTTL:          "10 минут",
			PasswordResetURL: "https://marketmesh.test/reset?t=abc",
		},
		TemplateRecoveryCode: CodeData{
			RecipientEmail:   "anna@example.ru",
			Code:             "482913",
			CodeTTL:          "10 минут",
			PasswordResetURL: "https://marketmesh.test/reset?t=abc",
		},
		TemplateNewLogin: NewLoginData{
			RecipientEmail:   "anna@example.ru",
			Timestamp:        "21 сентября 2026, 14:03, UTC+3",
			Device:           "MacBook, macOS",
			Browser:          "Chrome 140",
			IP:               "203.0.113.10",
			Location:         "Москва, Россия",
			PasswordResetURL: "https://marketmesh.test/reset?t=abc",
		},
		TemplatePasswordReset: PasswordResetData{
			RecipientEmail: "anna@example.ru",
			ResetURL:       "https://marketmesh.test/reset?t=abc",
			LinkTTL:        "2 часа",
		},
		TemplatePasswordChanged: PasswordChangedData{
			RecipientEmail: "anna@example.ru",
			Timestamp:      "21 сентября 2026, 14:03, UTC+3",
			DeviceBrowser:  "MacBook, Chrome 140",
			IP:             "203.0.113.10",
			RecoveryURL:    "https://marketmesh.test/recover?t=abc",
		},
		TemplateEmailChangeAlert: EmailChangeAlertData{
			OldEmail:  "anna@example.ru",
			NewEmail:  "anna.new@example.ru",
			Timestamp: "21 сентября 2026, 14:03, UTC+3",
			IP:        "203.0.113.10",
			CancelURL: "https://marketmesh.test/email/cancel?t=abc",
			CancelTTL: "7 дней",
		},
		TemplateEmailChangeConfirm: EmailChangeConfirmData{
			OldEmail:   "anna@example.ru",
			NewEmail:   "anna.new@example.ru",
			ConfirmURL: "https://marketmesh.test/email/confirm?t=abc",
			LinkTTL:    "24 часа",
		},
		TemplateAccountLocked: AccountLockedData{
			RecipientEmail:   "anna@example.ru",
			LockDuration:     "30 минут",
			Timestamp:        "21 сентября 2026, 14:03, UTC+3",
			Attempts:         5,
			IP:               "203.0.113.10",
			Location:         "Москва, Россия",
			PasswordResetURL: "https://marketmesh.test/reset?t=abc",
		},
		TemplateSessionsClosed: SessionsClosedData{
			RecipientEmail: "anna@example.ru",
			Timestamp:      "21 сентября 2026, 14:03, UTC+3",
			LoginURL:       "https://marketmesh.test/login",
		},
		TemplateTwoFactorOn: TwoFactorOnData{
			RecipientEmail: "anna@example.ru",
			BackupCodesURL: "https://marketmesh.test/account/security/codes",
		},
		TemplateTwoFactorOff: TwoFactorOffData{
			RecipientEmail: "anna@example.ru",
			Timestamp:      "21 сентября 2026, 14:03, UTC+3",
			DeviceBrowser:  "MacBook, Chrome 140",
			IP:             "203.0.113.10",
			EnableURL:      "https://marketmesh.test/account/security/2fa",
		},
		TemplateBackupCodes: BackupCodesData{
			RecipientEmail: "anna@example.ru",
			CodesCount:     8,
			BackupCodesURL: "https://marketmesh.test/account/security/codes",
		},
		TemplateSellerDecision: SellerDecisionData{
			ShopName:  "Глиняные истории",
			Approved:  true,
			PortalURL: "https://seller.marketmesh.test/login",
			GuideURL:  "https://seller.marketmesh.test/guide",
		},
		TemplateStaffInvite: StaffInviteData{
			InviterName: "Анна Соколова",
			Role:        "модератор",
			InviteURL:   "https://staff.marketmesh.test/invite?t=abc",
			InviteTTL:   "7 дней",
		},
		TemplateAccountDeleted: AccountDeletedData{
			RecipientEmail: "anna@example.ru",
			DeletionDate:   "21 октября 2026",
			CancelURL:      "https://marketmesh.test/account/restore?t=abc",
		},
	}
}

func TestRenderAllTemplates(t *testing.T) {
	r, err := NewRenderer(testBrand())
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	data := fixtures()
	if len(data) != len(Templates()) {
		t.Fatalf("fixtures cover %d templates, want %d", len(data), len(Templates()))
	}
	for _, tpl := range Templates() {
		t.Run(string(tpl), func(t *testing.T) {
			msg, err := r.Render(tpl, data[tpl])
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if msg.Subject == "" || msg.Preheader == "" {
				t.Errorf("subject and preheader must not be empty")
			}
			if want := "MarketMesh <noreply@marketmesh.test>"; msg.From != want {
				t.Errorf("From = %q, want %q", msg.From, want)
			}
			for _, part := range [][]byte{msg.HTML, msg.Text} {
				if bytes.Contains(part, []byte("{{")) || bytes.Contains(part, []byte("[[")) {
					t.Errorf("unrendered placeholder left in output")
				}
			}
			if !bytes.Contains(msg.HTML, []byte("MarketMesh")) {
				t.Errorf("HTML must contain the brand")
			}
			if !bytes.Contains(msg.HTML, []byte("<table")) {
				t.Errorf("HTML must be table-based for mail clients")
			}
			if !bytes.Contains(msg.HTML, []byte("prefers-color-scheme")) {
				t.Errorf("HTML must carry the dark-theme media query")
			}
			if !strings.Contains(string(msg.HTML), msg.Preheader) {
				t.Errorf("HTML must embed the preheader")
			}
		})
	}
}

func TestRenderSellerDecisionRejected(t *testing.T) {
	r, err := NewRenderer(testBrand())
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	msg, err := r.Render(TemplateSellerDecision, SellerDecisionData{
		ShopName:        "Глиняные истории",
		Approved:        false,
		PortalURL:       "https://seller.marketmesh.test/apply",
		RejectReason:    "ИНН не проходит проверку ФНС.",
		FixInstructions: "Проверьте ИНН и подайте заявку снова.",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(msg.Subject, "отклонена") {
		t.Errorf("subject = %q, want rejection wording", msg.Subject)
	}
	if !strings.Contains(string(msg.HTML), "ИНН не проходит проверку ФНС.") {
		t.Errorf("HTML must contain the reject reason")
	}
	if !strings.Contains(string(msg.Text), "Что поправить") {
		t.Errorf("text part must contain fix instructions")
	}
}

func TestRenderEscapesHTML(t *testing.T) {
	r, err := NewRenderer(testBrand())
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	msg, err := r.Render(TemplateSellerDecision, SellerDecisionData{
		ShopName:  `<script>alert(1)</script>`,
		Approved:  true,
		PortalURL: "https://seller.marketmesh.test/login",
		GuideURL:  "https://seller.marketmesh.test/guide",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if bytes.Contains(msg.HTML, []byte("<script>alert(1)</script>")) {
		t.Errorf("HTML must escape injected markup")
	}
	if !bytes.Contains(msg.HTML, []byte("&lt;script&gt;")) {
		t.Errorf("HTML must contain the escaped shop name")
	}
}

func TestRenderErrors(t *testing.T) {
	r, err := NewRenderer(testBrand())
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	if _, err := r.Render("no_such_template", fixtures()[TemplateVerify]); err == nil {
		t.Errorf("unknown template must fail")
	}
	if _, err := r.Render(TemplateVerify, fixtures()[TemplateCode]); err == nil {
		t.Errorf("wrong data type must fail")
	}
	if _, err := r.Render(TemplateVerify, VerifyData{}); err == nil {
		t.Errorf("invalid data must fail validation")
	}
	badCode := fixtures()[TemplateCode].(CodeData)
	badCode.Code = "48x913"
	if _, err := r.Render(TemplateCode, badCode); err == nil {
		t.Errorf("non-digit code must fail validation")
	}
}

func TestNewRendererValidatesBrand(t *testing.T) {
	if _, err := NewRenderer(Brand{}); err == nil {
		t.Errorf("empty brand must fail")
	}
}

func TestRenderDeterministic(t *testing.T) {
	r, err := NewRenderer(testBrand())
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	first, err := r.Render(TemplateVerify, fixtures()[TemplateVerify])
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	second, err := r.Render(TemplateVerify, fixtures()[TemplateVerify])
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Equal(first.HTML, second.HTML) || !bytes.Equal(first.Text, second.Text) ||
		first.Subject != second.Subject {
		t.Errorf("render must be deterministic")
	}
}
