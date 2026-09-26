import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "FoC Supplier Showcase",
  description: "A local showcase for Friend on Campus suppliers.",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
