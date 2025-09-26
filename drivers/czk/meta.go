package czk

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	APIKey    string `json:"api_key" required:"true"`
	APISecret string `json:"api_secret" required:"true"`
}

var config = driver.Config{
	Name:        "CZK",
	LocalSort:   false,
	OnlyProxy:   false,
	NoCache:     false,
	NoUpload:    true,
	NeedMs:      false,
	DefaultRoot: "0",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &CZK{}
	})
}