# custom-rc42

Base: upstream QuantumNous/new-api tag v1.0.0-rc.42.

Custom modifications carried on this branch:

- responses_via_chat_completions (RVC): channel toggle, web UI and i18n.
- Static WebP to PNG normalization before forwarding upstream.
- Reasoning history (summary list) handling for Responses<->Chat; reasoning-only
  output kept out of visible content.
- Server tools with a name downgraded to function tools; inner `type` stripped
  from json_schema.
- Stream error interception narrowed to overload-class errors; business errors and
  response.failed forwarded to the client unchanged.
- input_image.image_url wrapped as an object when converting Responses to Chat.
- Responses->Chat conversion that yields no messages rejected locally.
- Responses `encrypted_content` parts converted to text parts; other unknown part
  types dropped with a diagnostic instead of being forwarded upstream.
- Chat `stop` kept as a string array.

Build: /home/yunluo7/build-rc42.sh builds new-api-custom:v1.0.0-rc.42-rvc2
from this worktree.
