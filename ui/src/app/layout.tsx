import type { Metadata } from "next";
import { Navigation } from "@/components/Navigation";
import "./globals.css";

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

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body className="min-h-screen bg-white text-gray-900">
        <div className="flex min-h-screen">
          <Navigation />
          <main className="flex-1 p-6">{children}</main>
        </div>
      </body>
    </html>
  );
}
