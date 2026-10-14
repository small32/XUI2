package model

// ClientSession stores only token hashes; changing an inbound password invalidates it.
type ClientSession struct {
	Id             int    `json:"-" gorm:"primaryKey"`
	AccountID      int    `json:"-" gorm:"index"`
	AccessHash     string `json:"-" gorm:"uniqueIndex"`
	RefreshHash    string `json:"-" gorm:"uniqueIndex"`
	CredentialHash string `json:"-"`
	AccessExpires  int64  `json:"-"`
	RefreshExpires int64  `json:"-"`
}

type ClientPackage struct {
	Id          int    `json:"id" gorm:"primaryKey"`
	Platform    string `json:"platform"`
	Arch        string `json:"arch"`
	Version     string `json:"version"`
	Filename    string `json:"filename"`
	StorageName string `json:"-"`
	ExternalURL string `json:"externalUrl"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Published   bool   `json:"published"`
	CreatedAt   int64  `json:"createdAt"`
}
