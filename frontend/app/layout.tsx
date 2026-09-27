import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Campus suppliers | Friend on Campus",
  description: "Find food, printing and everyday essentials across NUS.",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
