import { defaultLocale, type Locale } from "./config";
import { formatMarketOutcome } from "../lib/markets";

const en = {
  "chooser.continue": "Continue",
  "chooser.detected": "We detected {language}.",
  "chooser.prompt": "Choose your preferred language",
  "chooser.title": "Language preference",
  "auth.forgot.summary": "Enter your account email. We will send a one-time password-reset link if it matches an account.",
  "auth.forgot.title": "Reset your password",
  "auth.login.summary": "Sign in to save teams, leagues, and matches across your devices.",
  "auth.login.title": "Welcome back",
  "auth.register.summary": "Create a free account, start saving football, and verify your email from the secure link we send.",
  "auth.register.title": "Create your account",
  "auth.reset.summary": "Use at least 8 characters. Saving a new password signs out every existing session.",
  "auth.reset.title": "Choose a new password",
  "auth.verify.request_summary": "Enter your email to request a fresh verification link.",
  "auth.verify.title": "Verify your email",
  "auth.verify.token_summary": "Confirm the verification link to activate account features.",
  "home.explore": "Explore today's predictions",
  "home.fixtures": "View fixtures",
  "home.hero_eyebrow": "Football intelligence, refreshed daily",
  "home.hero_emphasis": "Read the signal.",
  "home.hero_title": "See the match.",
  "home.summary": "Data-led football predictions with clear confidence and versioned outputs.",
  "language.label": "Language",
  "nav.accas": "ACCAs",
  "nav.account": "Account",
  "nav.builder": "Builder",
  "nav.fixtures": "Fixtures",
  "nav.home": "Home",
  "nav.predictions": "Predictions",
  "nav.saved": "Saved",
  "nav.search": "Search",
  "nav.sign_in": "Sign in",
  "nav.in_play": "In-play",
  "nav.upgrade": "Go Pro",
  "prediction.away_win": "Away win",
  "prediction.btts_no": "Both teams to score: No",
  "prediction.btts_yes": "Both teams to score: Yes",
  "prediction.draw": "Draw",
  "prediction.home_win": "Home win",
  "search.aria": "Search teams, leagues, matches, or countries",
  "search.placeholder": "Search teams, leagues, matches",
  "search.submit": "Search",
  "site.description": "Data-led football predictions and fixture intelligence.",
  "site.title": "SokaLens - Football predictions built on data",
  "match.all_predictions": "All predictions",
  "match.current_selection": "{market} is the current selection for this match.",
  "match.metadata_description": "Match details, prediction confidence, odds, and available analysis for {home} vs {away}.",
  "match.metadata_title": "{home} vs {away} prediction - SokaLens",
  "match.no_prediction": "No published prediction",
  "match.prediction": "Prediction",
  "today.metadata_description": "Today's football predictions with confidence, market, risk, and reasoning.",
  "today.metadata_title": "Today's football predictions - SokaLens",
  "today.summary": "Filter today's football predictions by country, league, and market.",
  /*
    The masthead names the page, it does not advertise the brand.

    Both of these served "Every signal. The prediction behind it." -- the design
    specifies "Today's football predictions", and these are two of the highest-
    intent queries on the property. The slogan is kept on the home hero, where a
    slogan belongs; here the h1 and the <title> finally agree.

    /predictions/tomorrow renders the Today page with `forcedDay`, so it needs
    its own key or it silently inherits today's heading -- which is exactly how
    both pages came to share one.
  */
  "today.title": "Today's football predictions",
  "tomorrow.title": "Tomorrow's football predictions",
  "tomorrow.summary": "Filter tomorrow's football predictions by country, league, and market.",
  "template.team_form": "{team} won {wins} of their last {matches} matches.",
} as const;

export type TranslationKey = keyof typeof en;
type Catalog = Record<TranslationKey, string>;

const es: Catalog = { ...en, "chooser.continue": "Continuar", "chooser.detected": "Detectamos {language}.", "chooser.prompt": "Elige tu idioma preferido", "chooser.title": "Preferencia de idioma", "language.label": "Idioma", "nav.account": "Cuenta", "nav.builder": "Constructor", "nav.fixtures": "Partidos", "nav.in_play": "En vivo", "nav.home": "Inicio", "nav.predictions": "Pronósticos", "nav.saved": "Guardado", "nav.search": "Buscar", "nav.sign_in": "Iniciar sesión", "nav.upgrade": "Hazte Pro", "prediction.away_win": "Victoria visitante", "prediction.btts_no": "Ambos equipos marcan: No", "prediction.btts_yes": "Ambos equipos marcan: Sí", "prediction.draw": "Empate", "prediction.home_win": "Victoria local", "search.aria": "Buscar equipos, ligas, partidos o países", "search.placeholder": "Buscar equipos, ligas, partidos", "search.submit": "Buscar", "site.description": "Pronósticos de fútbol basados en datos e información de partidos.", "site.title": "SokaLens - Pronósticos de fútbol basados en datos", "template.team_form": "{team} ganó {wins} de sus últimos {matches} partidos." };
const fr: Catalog = { ...en, "chooser.continue": "Continuer", "chooser.detected": "Nous avons détecté {language}.", "chooser.prompt": "Choisissez votre langue", "chooser.title": "Préférence linguistique", "language.label": "Langue", "nav.account": "Compte", "nav.builder": "Combiné", "nav.fixtures": "Matchs", "nav.in_play": "En direct", "nav.home": "Accueil", "nav.predictions": "Pronostics", "nav.saved": "Favoris", "nav.search": "Rechercher", "nav.sign_in": "Connexion", "nav.upgrade": "Passer Pro", "prediction.away_win": "Victoire à l’extérieur", "prediction.btts_no": "Les deux équipes marquent : Non", "prediction.btts_yes": "Les deux équipes marquent : Oui", "prediction.draw": "Match nul", "prediction.home_win": "Victoire à domicile", "search.aria": "Rechercher des équipes, ligues, matchs ou pays", "search.placeholder": "Rechercher équipes, ligues, matchs", "search.submit": "Rechercher", "site.description": "Pronostics de football fondés sur les données et informations sur les matchs.", "site.title": "SokaLens - Pronostics de football fondés sur les données", "template.team_form": "{team} a gagné {wins} de ses {matches} derniers matchs." };
const pt: Catalog = { ...en, "chooser.continue": "Continuar", "chooser.detected": "Detetámos {language}.", "chooser.prompt": "Escolha o seu idioma preferido", "chooser.title": "Preferência de idioma", "language.label": "Idioma", "nav.account": "Conta", "nav.builder": "Construtor", "nav.fixtures": "Jogos", "nav.in_play": "Ao vivo", "nav.home": "Início", "nav.predictions": "Previsões", "nav.saved": "Guardados", "nav.search": "Pesquisar", "nav.sign_in": "Entrar", "nav.upgrade": "Aderir ao Pro", "prediction.away_win": "Vitória visitante", "prediction.btts_no": "Ambas marcam: Não", "prediction.btts_yes": "Ambas marcam: Sim", "prediction.draw": "Empate", "prediction.home_win": "Vitória da casa", "search.aria": "Pesquisar equipas, ligas, jogos ou países", "search.placeholder": "Pesquisar equipas, ligas, jogos", "search.submit": "Pesquisar", "site.description": "Previsões de futebol baseadas em dados e informação de jogos.", "site.title": "SokaLens - Previsões de futebol baseadas em dados", "template.team_form": "{team} venceu {wins} dos últimos {matches} jogos." };
const ar: Catalog = { ...en, "chooser.continue": "متابعة", "chooser.detected": "اكتشفنا اللغة: {language}.", "chooser.prompt": "اختر لغتك المفضلة", "chooser.title": "تفضيل اللغة", "language.label": "اللغة", "nav.account": "الحساب", "nav.builder": "منشئ الرهان", "nav.fixtures": "المباريات", "nav.in_play": "مباشر", "nav.home": "الرئيسية", "nav.predictions": "التوقعات", "nav.saved": "المحفوظة", "nav.search": "بحث", "nav.sign_in": "تسجيل الدخول", "nav.upgrade": "الترقية إلى Pro", "prediction.away_win": "فوز الضيف", "prediction.btts_no": "يسجل الفريقان: لا", "prediction.btts_yes": "يسجل الفريقان: نعم", "prediction.draw": "تعادل", "prediction.home_win": "فوز صاحب الأرض", "search.aria": "ابحث عن الفرق أو الدوريات أو المباريات أو الدول", "search.placeholder": "ابحث عن الفرق والدوريات والمباريات", "search.submit": "بحث", "site.description": "توقعات كرة قدم قائمة على البيانات ومعلومات المباريات.", "site.title": "SokaLens - توقعات كرة قدم مبنية على البيانات", "template.team_form": "فاز {team} في {wins} من آخر {matches} مباريات." };
const de: Catalog = { ...en, "chooser.continue": "Weiter", "chooser.detected": "Wir haben {language} erkannt.", "chooser.prompt": "Wähle deine bevorzugte Sprache", "chooser.title": "Spracheinstellung", "language.label": "Sprache", "nav.account": "Konto", "nav.builder": "Wettschein", "nav.fixtures": "Spiele", "nav.in_play": "Live", "nav.home": "Start", "nav.predictions": "Prognosen", "nav.saved": "Gespeichert", "nav.search": "Suche", "nav.sign_in": "Anmelden", "nav.upgrade": "Pro werden", "prediction.away_win": "Auswärtssieg", "prediction.btts_no": "Beide Teams treffen: Nein", "prediction.btts_yes": "Beide Teams treffen: Ja", "prediction.draw": "Unentschieden", "prediction.home_win": "Heimsieg", "search.aria": "Teams, Ligen, Spiele oder Länder suchen", "search.placeholder": "Teams, Ligen, Spiele suchen", "search.submit": "Suchen", "site.description": "Datenbasierte Fußballprognosen und Spielinformationen.", "site.title": "SokaLens - Datenbasierte Fußballprognosen", "template.team_form": "{team} gewann {wins} der letzten {matches} Spiele." };
const it: Catalog = { ...en, "chooser.continue": "Continua", "chooser.detected": "Abbiamo rilevato {language}.", "chooser.prompt": "Scegli la lingua preferita", "chooser.title": "Preferenza lingua", "language.label": "Lingua", "nav.account": "Account", "nav.builder": "Schedina", "nav.fixtures": "Partite", "nav.in_play": "Live", "nav.home": "Home", "nav.predictions": "Pronostici", "nav.saved": "Salvati", "nav.search": "Cerca", "nav.sign_in": "Accedi", "nav.upgrade": "Passa a Pro", "prediction.away_win": "Vittoria ospite", "prediction.btts_no": "Entrambe segnano: No", "prediction.btts_yes": "Entrambe segnano: Sì", "prediction.draw": "Pareggio", "prediction.home_win": "Vittoria casa", "search.aria": "Cerca squadre, campionati, partite o paesi", "search.placeholder": "Cerca squadre, campionati, partite", "search.submit": "Cerca", "site.description": "Pronostici di calcio basati sui dati e informazioni sulle partite.", "site.title": "SokaLens - Pronostici di calcio basati sui dati", "template.team_form": "{team} ha vinto {wins} delle ultime {matches} partite." };

const criticalOverrides: Record<Exclude<Locale, "en">, Partial<Catalog>> = {
  es: {
    "auth.forgot.summary": "Introduce el correo de tu cuenta. Enviaremos un enlace único si coincide con una cuenta.", "auth.forgot.title": "Restablece tu contraseña",
    "auth.login.summary": "Inicia sesión para guardar equipos, ligas y partidos en todos tus dispositivos.", "auth.login.title": "Te damos la bienvenida",
    "auth.register.summary": "Crea una cuenta gratuita, guarda fútbol y verifica tu correo con el enlace seguro.", "auth.register.title": "Crea tu cuenta",
    "auth.reset.summary": "Usa al menos 8 caracteres. La nueva contraseña cerrará las demás sesiones.", "auth.reset.title": "Elige una nueva contraseña",
    "auth.verify.request_summary": "Introduce tu correo para solicitar otro enlace de verificación.", "auth.verify.title": "Verifica tu correo", "auth.verify.token_summary": "Confirma el enlace para activar las funciones de tu cuenta.",
    "home.explore": "Ver los pronósticos de hoy", "home.fixtures": "Ver partidos", "home.hero_eyebrow": "Inteligencia futbolística, actualizada a diario", "home.hero_emphasis": "Lee la señal.", "home.hero_title": "Mira el partido.", "home.summary": "Pronósticos de fútbol basados en datos, con confianza clara y resultados versionados.",
    "match.all_predictions": "Todos los pronósticos", "match.current_selection": "{market} es la selección actual para este partido.", "match.metadata_description": "Detalles, confianza, cuotas y análisis disponible para {home} contra {away}.", "match.metadata_title": "Pronóstico {home} vs {away} - SokaLens", "match.no_prediction": "Sin pronóstico publicado", "match.prediction": "Pronóstico",
    "today.metadata_description": "Pronósticos de fútbol de hoy con confianza, mercado, riesgo y explicación.", "today.metadata_title": "Pronósticos de fútbol de hoy - SokaLens", "today.summary": "Filtra los pronósticos de hoy por país, liga y mercado.", "today.title": "Pronósticos de fútbol de hoy", "tomorrow.title": "Pronósticos de fútbol de mañana", "tomorrow.summary": "Filtra los pronósticos de mañana por país, liga y mercado.",
  },
  fr: {
    "auth.forgot.summary": "Saisissez l’e-mail du compte. Nous enverrons un lien unique s’il correspond à un compte.", "auth.forgot.title": "Réinitialiser votre mot de passe", "auth.login.summary": "Connectez-vous pour enregistrer équipes, ligues et matchs sur vos appareils.", "auth.login.title": "Bon retour", "auth.register.summary": "Créez un compte gratuit, enregistrez vos favoris et vérifiez votre e-mail avec le lien sécurisé.", "auth.register.title": "Créer votre compte", "auth.reset.summary": "Utilisez au moins 8 caractères. Le nouveau mot de passe déconnecte les autres sessions.", "auth.reset.title": "Choisir un nouveau mot de passe", "auth.verify.request_summary": "Saisissez votre e-mail pour demander un nouveau lien de vérification.", "auth.verify.title": "Vérifier votre e-mail", "auth.verify.token_summary": "Confirmez le lien pour activer les fonctions du compte.",
    "home.explore": "Voir les pronostics du jour", "home.fixtures": "Voir les matchs", "home.hero_eyebrow": "Intelligence football, actualisée chaque jour", "home.hero_emphasis": "Lisez le signal.", "home.hero_title": "Voyez le match.", "home.summary": "Pronostics fondés sur les données, avec confiance claire et résultats versionnés.", "match.all_predictions": "Tous les pronostics", "match.current_selection": "{market} est la sélection actuelle pour ce match.", "match.metadata_description": "Détails, confiance, cotes et analyse disponible pour {home} contre {away}.", "match.metadata_title": "Pronostic {home} vs {away} - SokaLens", "match.no_prediction": "Aucun pronostic publié", "match.prediction": "Pronostic", "today.metadata_description": "Pronostics du jour avec confiance, marché, risque et explication.", "today.metadata_title": "Pronostics football du jour - SokaLens", "today.summary": "Filtrez les pronostics du jour par pays, ligue et marché.", "today.title": "Pronostics football du jour", "tomorrow.title": "Pronostics football de demain", "tomorrow.summary": "Filtrez les pronostics de demain par pays, ligue et marché.",
  },
  pt: {
    "auth.forgot.summary": "Introduza o e-mail da conta. Enviaremos um link único se corresponder a uma conta.", "auth.forgot.title": "Repor a palavra-passe", "auth.login.summary": "Entre para guardar equipas, ligas e jogos em todos os dispositivos.", "auth.login.title": "Bem-vindo de volta", "auth.register.summary": "Crie uma conta gratuita, guarde futebol e verifique o e-mail pelo link seguro.", "auth.register.title": "Criar conta", "auth.reset.summary": "Use pelo menos 8 caracteres. A nova palavra-passe termina as outras sessões.", "auth.reset.title": "Escolher nova palavra-passe", "auth.verify.request_summary": "Introduza o e-mail para pedir um novo link de verificação.", "auth.verify.title": "Verificar e-mail", "auth.verify.token_summary": "Confirme o link para ativar as funções da conta.",
    "home.explore": "Ver previsões de hoje", "home.fixtures": "Ver jogos", "home.hero_eyebrow": "Inteligência de futebol, atualizada diariamente", "home.hero_emphasis": "Leia o sinal.", "home.hero_title": "Veja o jogo.", "home.summary": "Previsões baseadas em dados, com confiança clara e resultados versionados.", "match.all_predictions": "Todas as previsões", "match.current_selection": "{market} é a seleção atual para este jogo.", "match.metadata_description": "Detalhes, confiança, odds e análise disponível para {home} contra {away}.", "match.metadata_title": "Previsão {home} vs {away} - SokaLens", "match.no_prediction": "Sem previsão publicada", "match.prediction": "Previsão", "today.metadata_description": "Previsões de hoje com confiança, mercado, risco e explicação.", "today.metadata_title": "Previsões de futebol de hoje - SokaLens", "today.summary": "Filtre as previsões de hoje por país, liga e mercado.", "today.title": "Previsões de futebol de hoje", "tomorrow.title": "Previsões de futebol de amanhã", "tomorrow.summary": "Filtre as previsões de amanhã por país, liga e mercado.",
  },
  ar: {
    "auth.forgot.summary": "أدخل بريد حسابك وسنرسل رابطًا صالحًا لمرة واحدة إذا وُجد الحساب.", "auth.forgot.title": "إعادة تعيين كلمة المرور", "auth.login.summary": "سجّل الدخول لحفظ الفرق والدوريات والمباريات على أجهزتك.", "auth.login.title": "مرحبًا بعودتك", "auth.register.summary": "أنشئ حسابًا مجانيًا واحفظ اختياراتك وتحقق من بريدك عبر الرابط الآمن.", "auth.register.title": "إنشاء حساب", "auth.reset.summary": "استخدم 8 أحرف على الأقل. كلمة المرور الجديدة تنهي الجلسات الأخرى.", "auth.reset.title": "اختر كلمة مرور جديدة", "auth.verify.request_summary": "أدخل بريدك لطلب رابط تحقق جديد.", "auth.verify.title": "تحقق من بريدك", "auth.verify.token_summary": "أكد الرابط لتفعيل مزايا الحساب.",
    "home.explore": "استكشف توقعات اليوم", "home.fixtures": "عرض المباريات", "home.hero_eyebrow": "ذكاء كروي يتجدد يوميًا", "home.hero_emphasis": "اقرأ الإشارة.", "home.hero_title": "شاهد المباراة.", "home.summary": "توقعات مبنية على البيانات بثقة واضحة ونتائج محفوظة الإصدارات.", "match.all_predictions": "كل التوقعات", "match.current_selection": "{market} هو الاختيار الحالي لهذه المباراة.", "match.metadata_description": "تفاصيل المباراة والثقة والاحتمالات والتحليل المتاح لمباراة {home} ضد {away}.", "match.metadata_title": "توقع {home} ضد {away} - SokaLens", "match.no_prediction": "لا يوجد توقع منشور", "match.prediction": "التوقع", "today.metadata_description": "توقعات كرة القدم اليوم مع الثقة والسوق والمخاطر والتفسير.", "today.metadata_title": "توقعات كرة القدم اليوم - SokaLens", "today.summary": "صفِّ توقعات اليوم حسب الدولة والدوري والسوق.", "today.title": "توقعات كرة القدم اليوم", "tomorrow.title": "توقعات كرة القدم غدًا", "tomorrow.summary": "صفِّ توقعات الغد حسب الدولة والدوري والسوق.",
  },
  de: {
    "auth.forgot.summary": "Gib deine Konto-E-Mail ein. Bei einem Treffer senden wir einen einmaligen Link.", "auth.forgot.title": "Passwort zurücksetzen", "auth.login.summary": "Melde dich an, um Teams, Ligen und Spiele geräteübergreifend zu speichern.", "auth.login.title": "Willkommen zurück", "auth.register.summary": "Erstelle ein kostenloses Konto, speichere Fußball und bestätige deine E-Mail über den sicheren Link.", "auth.register.title": "Konto erstellen", "auth.reset.summary": "Nutze mindestens 8 Zeichen. Das neue Passwort beendet alle anderen Sitzungen.", "auth.reset.title": "Neues Passwort wählen", "auth.verify.request_summary": "Gib deine E-Mail ein, um einen neuen Bestätigungslink anzufordern.", "auth.verify.title": "E-Mail bestätigen", "auth.verify.token_summary": "Bestätige den Link, um die Kontofunktionen zu aktivieren.",
    "home.explore": "Heutige Prognosen ansehen", "home.fixtures": "Spiele ansehen", "home.hero_eyebrow": "Fußball-Intelligenz, täglich aktualisiert", "home.hero_emphasis": "Lies das Signal.", "home.hero_title": "Sieh das Spiel.", "home.summary": "Datenbasierte Prognosen mit klarer Konfidenz und versionierten Ergebnissen.", "match.all_predictions": "Alle Prognosen", "match.current_selection": "{market} ist die aktuelle Auswahl für dieses Spiel.", "match.metadata_description": "Spieldetails, Konfidenz, Quoten und verfügbare Analyse für {home} gegen {away}.", "match.metadata_title": "Prognose {home} vs {away} - SokaLens", "match.no_prediction": "Keine Prognose veröffentlicht", "match.prediction": "Prognose", "today.metadata_description": "Heutige Fußballprognosen mit Konfidenz, Markt, Risiko und Begründung.", "today.metadata_title": "Heutige Fußballprognosen - SokaLens", "today.summary": "Filtere heutige Prognosen nach Land, Liga und Markt.", "today.title": "Heutige Fußballprognosen", "tomorrow.title": "Morgige Fußballprognosen", "tomorrow.summary": "Filtere morgige Prognosen nach Land, Liga und Markt.",
  },
  it: {
    "auth.forgot.summary": "Inserisci l’e-mail dell’account. Invieremo un link monouso se corrisponde a un account.", "auth.forgot.title": "Reimposta la password", "auth.login.summary": "Accedi per salvare squadre, campionati e partite su tutti i dispositivi.", "auth.login.title": "Bentornato", "auth.register.summary": "Crea un account gratuito, salva il calcio e verifica l’e-mail con il link sicuro.", "auth.register.title": "Crea il tuo account", "auth.reset.summary": "Usa almeno 8 caratteri. La nuova password disconnette le altre sessioni.", "auth.reset.title": "Scegli una nuova password", "auth.verify.request_summary": "Inserisci l’e-mail per richiedere un nuovo link di verifica.", "auth.verify.title": "Verifica l’e-mail", "auth.verify.token_summary": "Conferma il link per attivare le funzioni dell’account.",
    "home.explore": "Esplora i pronostici di oggi", "home.fixtures": "Vedi partite", "home.hero_eyebrow": "Intelligenza calcistica, aggiornata ogni giorno", "home.hero_emphasis": "Leggi il segnale.", "home.hero_title": "Guarda la partita.", "home.summary": "Pronostici basati sui dati con confidenza chiara e risultati versionati.", "match.all_predictions": "Tutti i pronostici", "match.current_selection": "{market} è la selezione attuale per questa partita.", "match.metadata_description": "Dettagli, confidenza, quote e analisi disponibile per {home} contro {away}.", "match.metadata_title": "Pronostico {home} vs {away} - SokaLens", "match.no_prediction": "Nessun pronostico pubblicato", "match.prediction": "Pronostico", "today.metadata_description": "Pronostici di oggi con confidenza, mercato, rischio e spiegazione.", "today.metadata_title": "Pronostici di calcio di oggi - SokaLens", "today.summary": "Filtra i pronostici di oggi per paese, campionato e mercato.", "today.title": "Pronostici di calcio di oggi", "tomorrow.title": "Pronostici di calcio di domani", "tomorrow.summary": "Filtra i pronostici di domani per paese, campionato e mercato.",
  },
};

const baseCatalogs: Record<Locale, Catalog> = { en, es, fr, pt, ar, de, it };
const catalogs = Object.fromEntries(supportedCatalogLocales().map((locale) => [locale, locale === "en" ? en : { ...baseCatalogs[locale], ...criticalOverrides[locale] }])) as Record<Locale, Catalog>;

function supportedCatalogLocales(): Locale[] { return ["en", "es", "fr", "pt", "ar", "de", "it"]; }

export function translate(locale: Locale, key: TranslationKey, values: Record<string, string | number> = {}) {
  const template = catalogs[locale]?.[key] ?? catalogs[defaultLocale][key] ?? key;
  return Object.entries(values).reduce((text, [name, value]) => text.replaceAll(`{${name}}`, String(value)), template);
}

export function predictionLabel(locale: Locale, marketKey: string, selection: string) {
  const normalizedMarket = marketKey.trim().toLowerCase();
  const normalizedSelection = selection.trim().toLowerCase();
  if (["1x2", "match_result"].includes(normalizedMarket)) {
    if (["home", "1"].includes(normalizedSelection)) return translate(locale, "prediction.home_win");
    if (["away", "2"].includes(normalizedSelection)) return translate(locale, "prediction.away_win");
    if (["draw", "x"].includes(normalizedSelection)) return translate(locale, "prediction.draw");
  }
  if (["btts", "both_teams_to_score"].includes(normalizedMarket)) {
    if (normalizedSelection === "yes") return translate(locale, "prediction.btts_yes");
    if (normalizedSelection === "no") return translate(locale, "prediction.btts_no");
  }
  return formatMarketOutcome(normalizedMarket, normalizedSelection, locale);
}
