;; Inputs are integer CNY minor units and a basis-point threshold.
;; ceil(standard * bps / 10000) is computed without overflowing i64.
;; No imports, memory, start function, floating point, or mutable state.
(module
  (func (export "approval") (param $standard i64) (param $offered i64) (param $bps i64) (result i64)
    (if (i32.or
          (i32.or (i64.eqz (local.get $standard))
                  (i64.gt_u (local.get $standard) (i64.const 9000000000000000)))
          (i32.or
            (i32.or (i64.eqz (local.get $offered))
                    (i64.gt_u (local.get $offered) (local.get $standard)))
            (i32.or (i64.eqz (local.get $bps))
                    (i64.gt_u (local.get $bps) (i64.const 10000)))))
      (then unreachable))
    (i64.extend_i32_u (i64.lt_u
      (local.get $offered)
      (i64.add
        (i64.mul (i64.div_u (local.get $standard) (i64.const 10000)) (local.get $bps))
        (i64.div_u
          (i64.add
            (i64.mul (i64.rem_u (local.get $standard) (i64.const 10000)) (local.get $bps))
            (i64.const 9999))
          (i64.const 10000)))))))
