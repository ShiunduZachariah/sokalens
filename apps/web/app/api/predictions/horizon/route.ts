import { NextRequest, NextResponse } from "next/server";
import { fetchTodayPredictionsWithProvider, nairobiDate } from "../../../data/livePredictions";

const allowedDates = () => new Set(Array.from({ length: 7 }, (_, offset) => nairobiDate(offset)));

export async function GET(request: NextRequest) {
  const date = request.nextUrl.searchParams.get("date") ?? "";
  const parsedPage = Number.parseInt(request.nextUrl.searchParams.get("page") ?? "1", 10);
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date) || !allowedDates().has(date)) {
    return NextResponse.json({ error: "date must be inside the rolling seven-day horizon" }, { status: 400 });
  }
  if (!Number.isFinite(parsedPage) || parsedPage < 1 || parsedPage > 25) {
    return NextResponse.json({ error: "page must be between 1 and 25" }, { status: 400 });
  }
  try {
    const result = await fetchTodayPredictionsWithProvider(100, date, parsedPage);
    return NextResponse.json({
      predictions: result.engine,
      total: result.total,
      page: result.page,
      limit: result.limit,
    });
  } catch {
    return NextResponse.json({ error: "prediction feed temporarily unavailable" }, { status: 502 });
  }
}
