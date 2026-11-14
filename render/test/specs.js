// Shared spec fixtures — no tests in this file (importing a test file
// would re-register its tests in the importer's run).

// A small self-contained Vega-Lite spec — inline data.values, no urls.
export const LINE_SPEC = {
  $schema: 'https://vega.github.io/schema/vega-lite/v5.json',
  title: 'Monthly sales',
  width: 400,
  height: 300,
  data: {
    values: [
      { m: 'Jan', v: 10 }, { m: 'Feb', v: 14 }, { m: 'Mar', v: 12 },
      { m: 'Apr', v: 18 }, { m: 'May', v: 16 }, { m: 'Jun', v: 22 },
    ],
  },
  mark: 'line',
  encoding: {
    x: { field: 'm', type: 'nominal' },
    y: { field: 'v', type: 'quantitative' },
  },
};

export const NO_TITLE_SPEC = { ...LINE_SPEC };
delete NO_TITLE_SPEC.title;

export const LAYERED_SPEC = {
  $schema: 'https://vega.github.io/schema/vega-lite/v5.json',
  title: 'Revenue by region',
  width: 480,
  height: 320,
  data: {
    values: [
      { r: 'EMEA', q: 'Q1', v: 30 }, { r: 'EMEA', q: 'Q2', v: 41 },
      { r: 'AMER', q: 'Q1', v: 22 }, { r: 'AMER', q: 'Q2', v: 35 },
      { r: 'APAC', q: 'Q1', v: 12 }, { r: 'APAC', q: 'Q2', v: 19 },
    ],
  },
  layer: [
    {
      mark: 'bar',
      encoding: {
        x: { field: 'q', type: 'nominal' },
        y: { field: 'v', type: 'quantitative' },
        color: { field: 'r', type: 'nominal' },
      },
    },
    {
      mark: 'line',
      encoding: {
        x: { field: 'q', type: 'nominal' },
        y: { field: 'v', type: 'quantitative' },
        detail: { field: 'r', type: 'nominal' },
      },
    },
  ],
};
