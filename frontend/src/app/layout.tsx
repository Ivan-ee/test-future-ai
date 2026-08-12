import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "test-future — криптопрогнозы",
  description:
    "Прототип предсказателя будущего: live-цены криптовалют и прозрачные прогнозы.",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="ru">
      <body>{children}</body>
    </html>
  );
}
