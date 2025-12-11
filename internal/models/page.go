package models

// Page represents a generic page content stored in DynamoDB
type Page struct {
	PK      string                 `dynamodbav:"PK" json:"-"`
	SK      string                 `dynamodbav:"SK" json:"-"`
	Content map[string]interface{} `dynamodbav:"content" json:"content"`
}

// GetPK returns the partition key for a page
func (p *Page) GetPK() string {
	return p.PK
}

// GetSK returns the sort key for a page
func (p *Page) GetSK() string {
	return p.SK
}

