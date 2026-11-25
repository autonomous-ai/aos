import { useEffect, useState } from "react";
import { getDeviceConfig } from "@/lib/api";
import type { ChannelLoadedState } from "@/hooks/setup/types";
import type { SetupChannelType } from "@/types";

// True when the backend placed this device in mainland China (config.in_china).
// Unknown or unreachable counts as false, so a device never loses a feature it had.
export function useInChina(): boolean {
  const [inChina, setInChina] = useState(false);
  useEffect(() => {
    let cancelled = false;
    getDeviceConfig()
      .then((cfg) => { if (!cancelled) setInChina(cfg.in_china === true); })
      .catch(() => { /* keep the default */ });
    return () => { cancelled = true; };
  }, []);
  return inChina;
}

// Chat apps hidden for new setup in China: Telegram is blocked there and Slack is unreliable.
// Discord goes through the cloud relay and stays.
const CHINA_HIDDEN_CHANNELS: ReadonlySet<string> = new Set(["telegram", "slack"]);

// Whether `channel` is offered in the channel picker. A channel the owner already configured, or
// the one currently selected, stays visible so nothing already set up disappears.
export function channelOffered(
  channel: "telegram" | "slack" | "discord" | "imessage",
  inChina: boolean,
  loaded: ChannelLoadedState,
  selected: SetupChannelType,
): boolean {
  if (!inChina || !CHINA_HIDDEN_CHANNELS.has(channel)) return true;
  if (selected === channel) return true;
  return channel === "telegram" ? loaded.teleToken : loaded.slackBotToken;
}
