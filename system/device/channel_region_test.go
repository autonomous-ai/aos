package device

import (
	"context"
	"errors"
	"testing"

	"go.autonomous.ai/os/system/domain"
	"go.autonomous.ai/os/system/server/config"
)

func TestChannelUnavailableInRegion(t *testing.T) {
	cn := &config.Config{Region: "CN"}
	for _, ch := range []string{domain.ChannelTelegram, domain.ChannelSlack, domain.ChannelWhatsapp} {
		if !channelUnavailableInRegion(cn, ch) {
			t.Errorf("%s should be refused for a new setup in China", ch)
		}
	}
	for _, ch := range []string{domain.ChannelDiscord, domain.ChannelIMessage} {
		if channelUnavailableInRegion(cn, ch) {
			t.Errorf("%s must stay available in China", ch)
		}
	}
	// Outside China nothing is refused.
	for _, ch := range []string{domain.ChannelTelegram, domain.ChannelSlack, domain.ChannelWhatsapp} {
		if channelUnavailableInRegion(&config.Config{}, ch) || channelUnavailableInRegion(&config.Config{Region: "US"}, ch) {
			t.Errorf("%s refused outside China", ch)
		}
	}
	// A channel the owner already configured is kept.
	configured := &config.Config{Region: "CN", TelegramBotToken: "t", SlackBotToken: "s", WhatsappUserID: "w"}
	for _, ch := range []string{domain.ChannelTelegram, domain.ChannelSlack, domain.ChannelWhatsapp} {
		if channelUnavailableInRegion(configured, ch) {
			t.Errorf("configured %s must not be refused", ch)
		}
	}
}

func TestAddChannelRefusedInChina(t *testing.T) {
	gw := &fakeGateway{supported: []string{domain.ChannelTelegram, domain.ChannelDiscord}}
	svc := newService(gw, &config.Config{Region: "CN"})
	_, err := svc.AddChannel(context.Background(), domain.AddChannelRequest{Channel: domain.ChannelTelegram, TelegramBotToken: "x", TelegramUserID: "1"})
	if !errors.Is(err, ErrChannelUnavailableInRegion) {
		t.Fatalf("err = %v, want ErrChannelUnavailableInRegion", err)
	}
	if len(gw.addCalls) != 0 || svc.config.TelegramBotToken != "" {
		t.Fatal("refused channel must not be persisted or applied")
	}
}

func TestUpdateConfigRefusesNewChannelInChina(t *testing.T) {
	t.Chdir(t.TempDir())
	svc := newService(&fakeGateway{}, &config.Config{Region: "CN", Channel: domain.ChannelDiscord})
	err := svc.UpdateConfig(domain.UpdateConfigRequest{Channel: domain.ChannelSlack, SlackBotToken: "x"})
	if !errors.Is(err, ErrChannelUnavailableInRegion) {
		t.Fatalf("err = %v, want ErrChannelUnavailableInRegion", err)
	}
}
