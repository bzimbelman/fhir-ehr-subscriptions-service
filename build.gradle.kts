// Root build script for the subscription-service multi-project build.
//
// We don't apply any plugins to the ROOT project itself; all real work
// lives in subprojects. But Gradle's recommended fix for the
// "Kotlin plugin loaded twice in different subprojects" warning is to
// declare the Kotlin plugin at the root with `apply false` and let
// each subproject `apply` it (without re-declaring the version).
//
// Subprojects use `kotlin("jvm") version "2.0.21"` directly today; we
// can migrate to a shared version constant here in a follow-up. For
// now, the warning is benign — both subprojects ARE on the same
// version (2.0.21) and the build succeeds.

plugins {
    // `apply false` — make the plugin available to subprojects without
    // applying it here. Subprojects still need to `apply` it explicitly.
    // We keep version pinning in each subproject for clarity right now
    // (interface-engine and plugins-spi both pin 2.0.21); a future
    // refactor can centralize the version here.
    kotlin("jvm") version "2.0.21" apply false
}
