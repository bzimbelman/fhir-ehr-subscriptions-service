import type { Metadata } from "next";
import { Navigation } from "@/components/Navigation";
import { Footer } from "@/extensions/Footer";
import { LicenseProvider } from "@/extensions/LicenseProvider";
import { seedDefaultRegistryWithBuiltins } from "@/extensions/defaultRegistrySetup";
import { loadLicenseState } from "@/lib/license/licenseClient";
import "./globals.css";

// Wire the FOSS builtin manifest into the process-wide registry
// once at module load. Idempotent -- safe across hot reloads.
seedDefaultRegistryWithBuiltins();

export const metadata: Metadata = {
  title: "subscription-service operator console",
  description: "Operator UI for subscription-service.",
};

/**
 * Force every route to render dynamically at request time. Without this,
 * Next.js prerenders protected pages at build time with an empty env,
 * which means `isOidcConfigured` evaluates to false and the page bundles
 * a static "redirect to /signin" response. At runtime that prerendered
 * 307 is served from the cache (`x-nextjs-cache: HIT`,
 * `x-nextjs-prerender: 1`), so even an authenticated user with valid
 * session cookies gets bounced back to /signin -- the cached response
 * is computed without ever consulting `auth()`. Ticket #424 surfaced
 * this when the e2e suite couldn't get past sign-in.
 *
 * Marking the root layout as `force-dynamic` cascades to every nested
 * page so the auth() check runs on every request.
 */
export const dynamic = "force-dynamic";

/**
 * Resolve the boot-time license state. Errors fall back to FOSS so a
 * license-server outage cannot prevent the UI from rendering -- the
 * `loadLicenseState()` impl already encodes that policy for any
 * customer who has actually configured a key. Wrapping in a
 * try/catch here belt-and-suspenders the case where the cache file
 * itself is corrupt or the surrounding Node environment is unusable.
 */
async function resolveInitialLicenseState() {
  try {
    return await loadLicenseState();
  } catch {
    return { kind: "foss" as const, reason: "no-license-key" as const };
  }
}

export default async function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const initialLicenseState = await resolveInitialLicenseState();

  return (
    <html lang="en">
      <body className="min-h-screen bg-white text-gray-900">
        <LicenseProvider initialState={initialLicenseState}>
          <div className="flex min-h-screen flex-col">
            <div className="flex flex-1">
              <Navigation />
              <main className="flex-1 p-6">{children}</main>
            </div>
            <Footer />
          </div>
        </LicenseProvider>
      </body>
    </html>
  );
}
