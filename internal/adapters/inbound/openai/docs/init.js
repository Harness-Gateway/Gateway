Scalar.createApiReference('#app', {
  url: '/openapi.json',
  servers: [{ url: window.location.origin, description: 'This gateway' }],
  proxyUrl: '',
  telemetry: false,
  agent: { disabled: true },
  withDefaultFonts: false,
  hideClientButton: true,
  modelsSectionLabel: 'Schemas',
  defaultOpenAllTags: true,
  showDeveloperTools: 'never',
});
