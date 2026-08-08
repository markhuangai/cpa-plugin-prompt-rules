# CPA Prompt Rules

[![test](https://github.com/markhuangai/cpa-plugin-prompt-rules/actions/workflows/test.yml/badge.svg)](https://github.com/markhuangai/cpa-plugin-prompt-rules/actions/workflows/test.yml)
[![release](https://github.com/markhuangai/cpa-plugin-prompt-rules/actions/workflows/release.yml/badge.svg)](https://github.com/markhuangai/cpa-plugin-prompt-rules/actions/workflows/release.yml)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

CPA Prompt Rules is a native [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) request-interceptor plugin. It ports the prompt injection and stripping behavior from `Z-M-Huang/CLIProxyAPI` into a standalone plugin that can run on upstream CPA.

The plugin edits the inbound protocol shape before credential selection. CPA still owns authentication, provider selection, protocol translation, retries, and transport.

## Features

- Inject text into the system prompt or the last natural-language user message.
- Strip text with Go RE2 regular expressions.
- Scope rules by model wildcard and inbound protocol.
- Handle OpenAI Chat Completions, OpenAI Responses, Claude, Gemini, and Interactions payloads.
- Run every strip rule before every inject rule.
- Avoid duplicate injection when the same request is processed again.
- Skip image generation, image editing, Responses compact, and nested host-model callback requests.
- Configure ordered rules through a dedicated CPA management page or the generic plugin config API without restarting the process.

## Compatibility

The module is built against `github.com/router-for-me/CLIProxyAPI/v7` v7.2.123. It advertises RPC schema v1 because it does not use the schema-v2 request-lifecycle additions. The CPA host must support native plugins and the `request_interceptor` capability. The configuration page also requires CPA's `management_api` capability and plugin resource menus.

Build the plugin for the same operating system and architecture as CPA. A Go `c-shared` library is not portable across OS or CPU targets.

## Configuration

```yaml
plugins:
  enabled: true
  dir: "/absolute/path/to/plugins"
  configs:
    prompt-rules:
      enabled: true
      priority: 100
      rules:
        - name: append-system-policy
          enabled: true
          models:
            - name: "gpt-*"
              protocol: openai
          target: system
          action: inject
          position: append
          content: |-

            Answer concisely.

        - name: strip-internal-marker
          enabled: true
          target: user
          action: strip
          pattern: "\\[internal-only\\].*"
```

`plugins.enabled` and `plugins.configs.prompt-rules.enabled` must both be true. The library basename must be exactly `prompt-rules` with the host extension: `.so`, `.dylib`, or `.dll`.

### Configuration UI

Version `0.2.0` registers a **Prompt Rules** page in CPA's management frontend. Open the Plugins section, select **Prompt Rules**, enter the CPA management key, and choose **Load configuration**. The page provides typed controls for rule order, prompt target, action, injection position, model wildcards, and source protocols.

**Save changes** validates the complete rule set with the plugin's Go configuration parser before applying a shallow patch through CPA. The patch updates `enabled`, `priority`, and `rules` without replacing plugin-store metadata or unrelated config fields. The same page is available directly at:

```text
/v0/resource/plugins/prompt-rules/config
```

The dashboard HTML is a public plugin resource so CPA can embed it in the frontend. Reading or changing configuration still requires the management key. The page keeps that key only in memory and does not use browser storage. CPA's Management API must be enabled and reachable from the browser.

### Rule fields

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Unique rule identifier. |
| `enabled` | yes | Enables this rule. |
| `models` | no | Model wildcard and optional source-protocol filters. Empty means all models and supported protocols. |
| `target` | yes | `system` or `user`. |
| `action` | yes | `inject` or `strip`. |
| `content` | inject | Literal injected text. Separators and newlines are not added automatically. |
| `marker` | no | Anchor text for injection. If absent, position is relative to the target boundary. |
| `position` | no | `append` by default, or `prepend`. With a marker, position is relative to the first marker. |
| `pattern` | strip | Go RE2 expression, limited to 1,024 bytes. |

Supported `models[].protocol` values are `openai`, `openai-response`, `claude`, `gemini`, and `interactions`. `gemini-cli` in configuration is normalized to `interactions` for migration compatibility.

Model wildcards use `*` only and are case-sensitive. A request such as `route(high)` is tested against both `route(high)` and `route`.

For `target: user`, tool-result and function-result messages are ignored when finding the last natural-language user message. Marker injection does nothing when the marker is absent. Boundary injection does nothing when the target already contains `content`; marker injection does nothing when `content` is already adjacent to any marker occurrence in the configured direction.

## Migrating From The Fork Configuration

Move the old top-level list under the plugin config. The rule objects do not change.

```yaml
# Before: Z-M-Huang/CLIProxyAPI fork
prompt-rules:
  - name: append-system-policy
    enabled: true
    target: system
    action: inject
    content: "Answer concisely."
```

```yaml
# After: upstream CPA plus this plugin
plugins:
  enabled: true
  dir: "plugins"
  configs:
    prompt-rules:
      enabled: true
      rules:
        - name: append-system-policy
          enabled: true
          target: system
          action: inject
          content: "Answer concisely."
```

The plugin also accepts `prompt-rules` instead of `rules` inside its own config for a staged migration. Do not provide both keys; registration fails rather than choosing one silently.

The fork-specific `/v0/management/prompt-rules` endpoints are not part of this plugin. The configuration page uses CPA's generic plugin endpoints, which remain available for automation:

```text
GET   /v0/management/plugins/prompt-rules/config
PUT   /v0/management/plugins/prompt-rules/config
PATCH /v0/management/plugins/prompt-rules/config
PATCH /v0/management/plugins/prompt-rules/enabled
```

## Build And Test

Go 1.26 and a working C compiler are required.

```bash
make check
make build
```

`make build` writes the host-platform library to `dist/prompt-rules.<ext>`. The default test suite covers strict config parsing, model/protocol matching, every supported request shape, strip-before-inject behavior, idempotency, skipped endpoints, the management page, and the native RPC registration path.

Run the opt-in black-box test against a local CPA source checkout:

```bash
CPA_SOURCE=../CLIProxyAPI \
  go test -tags=integration -run TestPromptRulesWithCLIProxyAPI -count=1 -v
```

The black-box test builds CPA and the native plugin in a temporary directory, starts a local mock OpenAI-compatible provider, loads the library through CPA, verifies the Prompt Rules menu and parser-backed validation endpoint, sends `/v1/chat/completions`, and confirms that the mock received the injected system text. It uses no external provider credentials. The test currently runs on Linux and macOS.

### Manual local installation

1. Run `make build` on the CPA host machine.
2. Copy `dist/prompt-rules.so` on Linux, `dist/prompt-rules.dylib` on macOS, or `dist/prompt-rules.dll` on Windows into the configured `plugins.dir`. CPA also scans `plugins.dir/<goos>/<goarch>`.
3. Add `plugins.configs.prompt-rules.enabled: true`. Add rules in YAML or through the Prompt Rules management page.
4. Start CPA with `go run ./cmd/server --config /path/to/config.yaml` or your normal CPA binary.
5. If the Management API is enabled, verify registration:

```bash
curl -fsS http://127.0.0.1:8317/v0/management/plugins \
  -H "Authorization: Bearer $MANAGEMENT_PASSWORD" \
  | jq '.plugins[] | select(.id == "prompt-rules")'
```

`registered`, `enabled`, and `effective_enabled` should all be true. A copied library is trusted in-process code; only load artifacts you built or verified.

## Publishing And Plugin Store Registration

The release workflow accepts tags such as `v0.2.0` and builds these CPA Plugin Store assets:

```text
prompt-rules_0.2.0_linux_amd64.zip
prompt-rules_0.2.0_linux_arm64.zip
prompt-rules_0.2.0_darwin_amd64.zip
prompt-rules_0.2.0_darwin_arm64.zip
prompt-rules_0.2.0_windows_amd64.zip
checksums.txt
```

Each zip contains exactly one root-level library named `prompt-rules.so`, `prompt-rules.dylib`, or `prompt-rules.dll`. The workflow embeds the tag version in plugin metadata and validates the archive layout before publishing.

To register the plugin publicly:

1. Push this repository to `https://github.com/markhuangai/cpa-plugin-prompt-rules`.
2. Create and push a `v<major>.<minor>.<patch>` tag. Confirm the GitHub release contains all five zips plus `checksums.txt`.
3. Fork `router-for-me/CLIProxyAPI-Plugins-Store` and add this object to `registry.json`:

```json
{
  "id": "prompt-rules",
  "name": "Prompt Rules",
  "description": "Injects or strips system and user prompt text before provider translation, with model and protocol scoping.",
  "author": "markhuangai",
  "repository": "https://github.com/markhuangai/cpa-plugin-prompt-rules",
  "homepage": "https://github.com/markhuangai/cpa-plugin-prompt-rules",
  "license": "MIT",
  "tags": ["Interceptor", "Prompt"]
}
```

4. Open a Plugin Store pull request that changes `registry.json`. Include the release tag and evidence that the platform archives and checksum file exist.

Do not add a `version` field unless the store maintainers request it. CPA discovers the latest version from the repository's newest published `v*` release.

After the registry PR is merged, install through the CPA management UI or `POST /v0/management/plugin-store/prompt-rules/install`, then configure rules from the plugin's **Prompt Rules** page or the generic config API.

## Security And Operational Notes

- Native plugins execute in the CPA process with CPA's permissions.
- Invalid or unknown YAML fields fail plugin registration. This prevents misspelled policies from being accepted silently.
- Regular expressions use Go's linear-time RE2 engine and are length-limited, but broad expressions can still remove more prompt text than intended.
- Prompt text is intentionally sent to the selected upstream after mutation. Do not inject secrets.
- The plugin skips nested `host.model.*` executions to prevent duplicate mutation when another plugin delegates execution back to CPA.

## License

MIT. See [LICENSE](LICENSE).
