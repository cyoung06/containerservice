# The console's shared Claude Code login (containers with "Claude login" on): a long-lived token.
if [ -s /etc/containerservice-claude/token ]; then
  CLAUDE_CODE_OAUTH_TOKEN=$(cat /etc/containerservice-claude/token)
  export CLAUDE_CODE_OAUTH_TOKEN
fi
