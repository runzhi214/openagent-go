// sessions plugin — agent:sessions example using the high-level Plugin trait.
//
// Demonstrates:
//   - on_session_init: set custom system prompt + max_turns per session
//   - on_session_destroy: cleanup notification
//   - host::keyring_get: read per-user config from system keyring

#![no_std]
#![no_main]

extern crate alloc;
use openagent_pdk::prelude::*;
use openagent_pdk::export::Plugin;

struct SessionsPlugin;
impl Plugin for SessionsPlugin {
    fn plugin_type() -> &'static str { "agent:sessions" }
    fn name() -> &'static str { "session-customizer" }
    fn description() -> &'static str { "Customizes agent config per session" }

    fn on_session_init(ctx: &SessionCtx) -> Result<Option<SessionConfig>, String> {
        host::log_info(&alloc::format!(
            "sessions: init session_id={} user_id={}",
            ctx.session_id, ctx.user_id
        ));

        // Read per-user prompt from keyring (optional).
        let mut prompts = Vec::new();
        let key = alloc::format!("prompt:{}", ctx.user_id);
        if let Ok(custom) = host::keyring_get("openagent", &key) {
            if !custom.is_empty() {
                prompts.push(alloc::format!("You are {}. {}", ctx.user_id, custom));
            }
        }

        if prompts.is_empty() {
            prompts.push(String::from("You are a helpful assistant."));
        }

        Ok(Some(SessionConfig {
            system_prompts: prompts,
            max_turns: 10,
            ..Default::default()
        }))
    }

    fn on_session_destroy(ctx: &SessionCtx) {
        host::log_info(&alloc::format!(
            "sessions: destroy session_id={}",
            ctx.session_id
        ));
    }
}

openagent_pdk::export!(SessionsPlugin);
