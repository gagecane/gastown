// Gas Town OpenCode plugin: hooks SessionStart/Compaction via events.
// Injects gt prime context into the system prompt via experimental.chat.system.transform.
export const GasTown = async ({ $, directory }) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  const gtBin = "gt";
  let didInit = false;

  let primePromise = null;

  const shellQuote = (value) => `'${String(value).replace(/'/g, `'\\''`)}'`;
  const eventSessionID = (event) => event?.properties?.info?.id || event?.sessionID || event?.session?.id || "";

  const captureRun = async (cmd) => {
    try {
      return await $`/bin/sh -lc ${cmd}`.cwd(directory).text();
    } catch (err) {
      console.error(`[gastown] ${cmd} failed`, err?.message || err);
      return "";
    }
  };

  const loadPrime = async (source = "startup", sessionID = "") => {
    const env = [`GT_HOOK_SOURCE=${shellQuote(source)}`];
    if (sessionID) {
      env.push(`GT_SESSION_ID=${shellQuote(sessionID)}`);
    }
    return await captureRun(`${env.join(" ")} ${shellQuote(gtBin)} prime --hook`);
  };

  return {
    event: async ({ event }) => {
      if (event?.type === "session.created") {
        if (didInit) return;
        didInit = true;
        primePromise = loadPrime("startup", eventSessionID(event));
      }
      if (event?.type === "session.compacted") {
        primePromise = loadPrime("compact", eventSessionID(event));
      }
      if (event?.type === "session.deleted") {
        const sessionID = event.properties?.info?.id;
        if (sessionID) {
          await captureRun(`${shellQuote(gtBin)} costs record --session ${shellQuote(sessionID)}`);
        }
      }
    },
    "experimental.chat.system.transform": async (input, output) => {
      if (!primePromise) {
        primePromise = loadPrime("startup");
      }
      const context = await primePromise;
      if (context) {
        output.system.push(context);
      } else {
        primePromise = null;
      }
    },
    "experimental.session.compacting": async ({ sessionID }, output) => {
      const roleDisplay = role || "unknown";
      output.context.push(`
## Gas Town Multi-Agent System

**After Compaction:** Run \`gt prime --hook\` to restore full context.
**Check Hook:** \`gt hook\` - if work present, execute immediately (GUPP).
**Role:** ${roleDisplay}
`);
    },
  };
};
