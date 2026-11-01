ALTER TABLE public.acca_slip_legs RESET (
  autovacuum_vacuum_scale_factor,
  autovacuum_analyze_scale_factor
);

ALTER TABLE public.acca_slips RESET (
  autovacuum_vacuum_scale_factor,
  autovacuum_analyze_scale_factor
);
