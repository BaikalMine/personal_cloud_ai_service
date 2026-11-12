package domain

import "time"

type GalleryDatasetImportReceipt struct {
	UserID         int64     `json:"-"`
	RequestID      string    `json:"request_id"`
	RequestSHA256  string    `json:"-"`
	MediaID        int64     `json:"-"`
	DatasetID      string    `json:"dataset_id"`
	ImageID        string    `json:"image_id"`
	Revision       int64     `json:"revision"`
	AlreadyPresent bool      `json:"already_present"`
	Cancelled      bool      `json:"cancelled"`
	CreatedAt      time.Time `json:"created_at"`
}

type GalleryDatasetImportParams struct {
	UserID           int64
	RequestID        string
	RequestSHA256    string
	MediaID          int64
	MediaSize        int64
	DatasetID        string
	ExpectedRevision int64
	Create           bool
	AssetID          string
}
