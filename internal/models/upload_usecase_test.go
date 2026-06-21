package models

import "testing"

func TestUploadUseCaseSK(t *testing.T) {
	if got := UploadUseCaseSK("dispute_photo"); got != "UPLOAD_USECASE!dispute_photo" {
		t.Errorf("UploadUseCaseSK() = %q, want UPLOAD_USECASE!dispute_photo", got)
	}
}

func TestUploadUseCaseAllowsEntityType(t *testing.T) {
	u := &UploadUseCase{AllowedEntityTypes: []string{"customer"}}
	if !u.AllowsEntityType("customer") {
		t.Error("expected customer allowed")
	}
	if u.AllowsEntityType("de") {
		t.Error("expected de denied")
	}
}

func TestUploadUseCaseAllowsMime(t *testing.T) {
	u := &UploadUseCase{AllowedMimeTypes: []string{"image/jpeg", "image/png"}}
	if !u.AllowsMime("image/png") {
		t.Error("expected image/png allowed")
	}
	if u.AllowsMime("application/pdf") {
		t.Error("expected application/pdf denied")
	}
}
