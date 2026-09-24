package config

// RequiresPatchedCore marks this distribution as one whose traffic and window
// quota rules depend on the bundled Xray patch. Official upstream panel and
// Xray updaters do not include that patch, so they must not replace either
// binary through the web API. A compatible release is installed as a matched
// panel/core pair by the distribution's deployment process.
const RequiresPatchedCore = true

const IncompatibleOfficialUpdate = "official web updates are disabled for this customized build; install a compatible panel and patched Xray core together"
