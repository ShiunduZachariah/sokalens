import { AuthForm } from "../components/account/AuthForm";
import { localizedAuthMetadata } from "../i18n/metadata";

export async function generateMetadata() { return localizedAuthMetadata("auth.forgot.title", "auth.forgot.summary", "/forgot-password"); }

export default function ForgotPasswordPage() {
  return <AuthForm mode="forgot" />;
}
