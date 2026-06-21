package models

// UploadUseCase is a registry entry that tells the generalized upload endpoint
// where a file for a given use case should go and what is allowed.
type UploadUseCase struct {
	UseCase            string   `json:"use_case" dynamodbav:"use_case"`
	Bucket             string   `json:"bucket" dynamodbav:"bucket"`
	KeyPrefix          string   `json:"key_prefix" dynamodbav:"key_prefix"`
	AllowedMimeTypes   []string `json:"allowed_mime_types" dynamodbav:"allowed_mime_types"`
	MaxFileSize        int64    `json:"max_file_size" dynamodbav:"max_file_size"`
	AllowedEntityTypes []string `json:"allowed_entity_types" dynamodbav:"allowed_entity_types"`
}

// UploadUseCaseSK is the sort key for a use-case registry item under DisputeConfigPK ("CONFIG").
func UploadUseCaseSK(useCase string) string { return "UPLOAD_USECASE!" + useCase }

func (u *UploadUseCase) AllowsEntityType(t string) bool {
	for _, e := range u.AllowedEntityTypes {
		if e == t {
			return true
		}
	}
	return false
}

func (u *UploadUseCase) AllowsMime(mime string) bool {
	for _, m := range u.AllowedMimeTypes {
		if m == mime {
			return true
		}
	}
	return false
}
