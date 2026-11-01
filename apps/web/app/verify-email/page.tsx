import { AuthForm } from "../components/account/AuthForm";
import { localizedAuthMetadata } from "../i18n/metadata";

export async function generateMetadata() { return localizedAuthMetadata("auth.verify.title", "auth.verify.request_summary", "/verify-email"); }

export default async function VerifyEmailPage({ searchParams }: { searchParams: Promise<{ token?: string }> }) {
  const { token } = await searchParams;
  return <AuthForm mode="verify" token={token} />;
}
