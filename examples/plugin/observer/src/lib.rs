// Observer logger plugin — agent:observers using the high-level Plugin trait.

#![no_std]
#![no_main]

extern crate alloc;
use openagent_pdk::prelude::*;
use openagent_pdk::export::Plugin;

struct LoggerPlugin;
impl Plugin for LoggerPlugin {
    fn plugin_type() -> &'static str { "agent:observers" }
    fn name() -> &'static str { "observer_logger" }

    fn stage_filter() -> (&'static str, &'static str) { ("*", "*") }

    fn observe_stage(event: &StageInput) -> StageOutput {
        host::log_info(&alloc::format!(
            "observer: stage={} phase={} error={}",
            event.name, event.phase, event.error
        ));
        StageOutput { action: String::from("continue"), reason: String::new() }
    }

    // Decorator consults are separate from stage events: the host calls
    // this synchronously when a provider API call fails with a status
    // code. Non-empty return REPLACES the error message shown to the
    // user (retry/retry-after semantics stay with the host).
    // The message is markdown-rendered by the frontend: emoji, bold, and
    // site-relative links ([text](/settings?q=model)) are supported.
    fn on_api_error(err: &ApiErrorInput) -> String {
        if err.status_code == 429 {
            host::log_info(&alloc::format!(
                "observer: rate limited ({} {} via {})",
                err.status_code, err.model_id, err.base_url
            ));
            String::from(
                "🚦 **服务繁忙（触发限流）**\n\n建议 [配置个人 provider](/settings?q=model) 后重试。",
            )
        } else {
            String::new()
        }
    }
}

openagent_pdk::export!(LoggerPlugin);
