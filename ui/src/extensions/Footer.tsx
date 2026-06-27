"use client";

/**
 * "What's loaded" footer renderer (master plan §3.2.1).
 *
 * Two shapes:
 *
 *   FOSS:
 *     subscription-service v1.4.0 · FOSS (Apache 2.0) · <repo url>
 *
 *   Active / stale-active license:
 *     subscription-service v1.4.0 · <tier> tier · entitled features:
 *     <list> · license expires <date>
 *
 * The version comes from the host's `package.json`; the tier and
 * entitlements come from the current `LicenseState`. The component
 * is intentionally inline-styled-free -- we use Tailwind because
 * this is the operator UI and not the embedded annotations applet.
 */

import { useLicenseState } from "./LicenseProvider";
import type { LicenseState } from "@/lib/license/types";

export interface FooterProps {
  /**
   * Override the rendered version (used by tests and Storybook).
   * Production callers leave this unset and the value baked in at
   * build time wins.
   */
  readonly version?: string;
  /**
   * Override the license state (used by tests). Production callers
   * leave this unset and the value from `LicenseProvider` wins.
   */
  readonly licenseState?: LicenseState;
}

const REPO_URL = "https://github.com/bzonfhir/subscription-service";
const PRODUCT_NAME = "subscription-service";
// Resolved at build time. We import the workspace `package.json`
// for the runtime version so a release bump in one place updates
// the footer everywhere.
//
// Webpack / turbopack treat this as a static JSON import and
// inline the value into the bundle.
import packageJson from "../../package.json";

function formatExpiry(date: Date): string {
  const yyyy = date.getUTCFullYear();
  const mm = String(date.getUTCMonth() + 1).padStart(2, "0");
  const dd = String(date.getUTCDate()).padStart(2, "0");
  return `${yyyy}-${mm}-${dd}`;
}

export function Footer(props: FooterProps): React.ReactElement {
  const fallbackState = useLicenseState();
  const state = props.licenseState ?? fallbackState;
  const version = props.version ?? packageJson.version;

  const baseLabel = `${PRODUCT_NAME} v${version}`;

  if (state.kind === "foss") {
    return (
      <footer
        aria-label="Build info"
        data-testid="ui-footer"
        className="border-t border-gray-200 bg-gray-50 px-6 py-3 text-xs text-gray-600"
      >
        <span>{baseLabel}</span>
        <span aria-hidden="true"> · </span>
        <span>FOSS (Apache 2.0)</span>
        <span aria-hidden="true"> · </span>
        <a
          href={REPO_URL}
          target="_blank"
          rel="noreferrer noopener"
          className="text-blue-700 hover:underline"
        >
          {REPO_URL}
        </a>
      </footer>
    );
  }

  // active or stale-active -- both have entitlements + tier + expiry
  const tier = state.info.tierName;
  const expires = formatExpiry(state.info.expiresAt);
  const features = state.entitlements.toArray();
  const featureList = features.length > 0 ? features.join(", ") : "(none)";

  return (
    <footer
      aria-label="Build info"
      data-testid="ui-footer"
      className="border-t border-gray-200 bg-gray-50 px-6 py-3 text-xs text-gray-600"
    >
      <span>{baseLabel}</span>
      <span aria-hidden="true"> · </span>
      <span>{tier} tier</span>
      <span aria-hidden="true"> · </span>
      <span>entitled features: {featureList}</span>
      <span aria-hidden="true"> · </span>
      <span>license expires {expires}</span>
    </footer>
  );
}
