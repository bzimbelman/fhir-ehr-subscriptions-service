import type { Metadata } from "next";
import { Navigation } from "@/components/Navigation";
import "./globals.css";

export const metadata: Metadata = {
  title: "subscription-service operator console",
  description: "Operator UI for subscription-service.",
};

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
