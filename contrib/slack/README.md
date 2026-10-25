# slack

Slack Web API. Setup: create a Slack app, add bot scopes (`chat:write`,
`channels:read`, `channels:history`, `reactions:write`), install to workspace,
store the `xoxb-` token as vault secret `SLACK_BOT_TOKEN` allowed for
`slack.com`, and grant `net.allow_hosts: ["slack.com"]`.
