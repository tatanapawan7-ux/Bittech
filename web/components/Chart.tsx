"use client";

import { useEffect, useRef } from "react";
import { createChart, ColorType, IChartApi, ISeriesApi, UTCTimestamp } from "lightweight-charts";
import { EngineEvent } from "@/lib/api";

// Chart aggregates the live trade stream into one-minute candles client-side
// (the MVP has no kline endpoint yet — Phase 4 follow-up adds server candles).
// It uses TradingView's open-source lightweight-charts library.
export function Chart({ trades }: { trades: EngineEvent[] }) {
  const ref = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<ISeriesApi<"Candlestick"> | null>(null);

  useEffect(() => {
    if (!ref.current) return;
    const chart = createChart(ref.current, {
      layout: { background: { type: ColorType.Solid, color: "#161a1e" }, textColor: "#9aa4af" },
      grid: { vertLines: { color: "#2b3139" }, horzLines: { color: "#2b3139" } },
      timeScale: { timeVisible: true, secondsVisible: false, borderColor: "#2b3139" },
      rightPriceScale: { borderColor: "#2b3139" },
      height: 320,
      autoSize: true,
    });
    const series = chart.addCandlestickSeries({
      upColor: "#16c784",
      downColor: "#ea3943",
      borderVisible: false,
      wickUpColor: "#16c784",
      wickDownColor: "#ea3943",
    });
    chartRef.current = chart;
    seriesRef.current = series;
    return () => chart.remove();
  }, []);

  useEffect(() => {
    if (!seriesRef.current) return;
    const candles = aggregate(trades);
    seriesRef.current.setData(candles);
  }, [trades]);

  return <div ref={ref} className="w-full" />;
}

type Candle = {
  time: UTCTimestamp;
  open: number;
  high: number;
  low: number;
  close: number;
};

// aggregate buckets trades into 60s candles. Without server timestamps we
// derive a pseudo-time from sequence so the demo chart renders progressively.
function aggregate(trades: EngineEvent[]): Candle[] {
  const byBucket = new Map<number, Candle>();
  const base = Math.floor(Date.now() / 1000) - trades.length * 2;
  trades.forEach((t, i) => {
    const time = (base + i * 2) as UTCTimestamp;
    const bucket = Math.floor(time / 60) * 60;
    const price = t.price ?? 0;
    const c = byBucket.get(bucket);
    if (!c) {
      byBucket.set(bucket, {
        time: bucket as UTCTimestamp,
        open: price,
        high: price,
        low: price,
        close: price,
      });
    } else {
      c.high = Math.max(c.high, price);
      c.low = Math.min(c.low, price);
      c.close = price;
    }
  });
  return [...byBucket.values()].sort((a, b) => a.time - b.time);
}
