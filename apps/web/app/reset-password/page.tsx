import { AuthForm } from "../components/account/AuthForm";
import { localizedAuthMetadata } from "../i18n/metadata";

export async function generateMetadata() { return localizedAuthMetadata("auth.reset.title", "auth.reset.summary", "/reset-password"); }

export default async function ResetPasswordPage({ searchParams }: { searchParams: Promise<{ token?: string }> }) {
  const { token } = await searchParams;
  return <AuthForm mode="reset" token={token} />;
}
