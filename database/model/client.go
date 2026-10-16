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
