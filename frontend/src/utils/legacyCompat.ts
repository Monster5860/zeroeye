import { createElement, Fragment, type ReactNode } from 'react';

/**
 * NATIVE COMPATIBILITY BOUNDARY FOR THE GENERATED FRONTEND API.
 *
 * The old migration layer mixed obsolete framework services, template string
 * execution, and unused formatting helpers. The active frontend only needs a
 * small fetch-compatible response shape and JSON serialization for the
 * generated API module, so this file now keeps that boundary explicit.
 */

export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE' | 'HEAD' | 'OPTIONS' | string;

export interface HttpLegacyConfig {
  method: HttpMethod;
  url: string;
  data?: unknown;
  params?: Record<string, string>;
  headers?: Record<string, string>;
  timeout?: number;
  withCredentials?: boolean;
  responseType?: XMLHttpRequestResponseType;
  transformRequest?: Array<(data: unknown) => unknown>;
  transformResponse?: Array<(data: unknown) => unknown>;
}

export interface HttpLegacyResponse<T> {
  data: T;
  status: number;
  statusText: string;
  headers: () => Record<string, string>;
  config: HttpLegacyConfig;
}

export interface HttpLegacyError {
  data: null;
  status: number;
  statusText: string;
  headers: () => Record<string, string>;
  config: HttpLegacyConfig;
  error: unknown;
}

export type TemplateValue = string | number | boolean | null | undefined;

function withQueryParams(url: string, params?: Record<string, string>): string {
  if (!params || Object.keys(params).length === 0) {
    return url;
  }

  const searchParams = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    searchParams.append(key, value);
  }

  const separator = url.includes('?') ? '&' : '?';
  return `${url}${separator}${searchParams.toString()}`;
}

function responseHeaders(response: Response): Record<string, string> {
  const headers: Record<string, string> = {};
  response.headers.forEach((value, key) => {
    headers[key] = value;
  });
  return headers;
}

function applyTransforms(value: unknown, transforms?: Array<(data: unknown) => unknown>): unknown {
  return transforms?.reduce((current, transform) => transform(current), value) ?? value;
}

function buildBody(config: HttpLegacyConfig): BodyInit | null {
  if (config.data === undefined) {
    return null;
  }

  const initialBody = typeof config.data === 'string' ? config.data : JSON.stringify(config.data);
  return applyTransforms(initialBody, config.transformRequest) as BodyInit;
}

async function parseResponse(response: Response, responseType?: XMLHttpRequestResponseType): Promise<unknown> {
  if (response.status === 204) {
    return null;
  }

  if (responseType === 'blob') {
    return response.blob();
  }

  if (responseType === 'arraybuffer') {
    return response.arrayBuffer();
  }

  if (responseType === 'text') {
    return response.text();
  }

  const contentType = response.headers.get('content-type') ?? '';
  if (contentType.includes('application/json')) {
    return response.json();
  }

  return response.text();
}

export async function $httpLegacy<T>(config: HttpLegacyConfig): Promise<HttpLegacyResponse<T>> {
  const controller = new AbortController();
  const timeoutId = config.timeout
    ? window.setTimeout(() => controller.abort(), config.timeout)
    : undefined;

  const headers: Record<string, string> = {
    Accept: 'application/json, text/plain, */*',
    ...config.headers,
  };

  if (config.data !== undefined && !headers['Content-Type']) {
    headers['Content-Type'] = 'application/json;charset=utf-8';
  }

  try {
    const response = await fetch(withQueryParams(config.url, config.params), {
      method: config.method,
      headers,
      body: buildBody(config),
      credentials: config.withCredentials ? 'include' : 'same-origin',
      signal: controller.signal,
    });

    const parsedData = await parseResponse(response, config.responseType);
    const data = applyTransforms(parsedData, config.transformResponse) as T;

    if (!response.ok) {
      throw {
        data: null,
        status: response.status,
        statusText: response.statusText,
        headers: () => responseHeaders(response),
        config,
        error: data,
      } satisfies HttpLegacyError;
    }

    return {
      data,
      status: response.status,
      statusText: response.statusText,
      headers: () => responseHeaders(response),
      config,
    };
  } catch (error: unknown) {
    if (typeof error === 'object' && error !== null && 'status' in error && 'config' in error) {
      throw error;
    }

    throw {
      data: null,
      status: -1,
      statusText: error instanceof Error ? error.message : 'Unknown request failure',
      headers: () => ({}),
      config,
      error,
    } satisfies HttpLegacyError;
  } finally {
    if (timeoutId !== undefined) {
      window.clearTimeout(timeoutId);
    }
  }
}

export function legacyToJson(value: unknown): string {
  return JSON.stringify(value, (_key, val: unknown) => (val === undefined ? null : val));
}

export function legacyFromJson<T>(json: string): T {
  return JSON.parse(json) as T;
}

/**
 * Render a simple tokenized template into React nodes without executing source
 * text. Tokens use {{name}} syntax and unmatched values render as empty text.
 */
export function renderLegacyTemplate(
  template: string,
  values: Record<string, TemplateValue> = {}
): ReactNode {
  const parts = template.split(/(\{\{\s*[\w.-]+\s*\}\})/g).filter(Boolean);
  const children = parts.map((part, index) => {
    const match = part.match(/^\{\{\s*([\w.-]+)\s*\}\}$/);
    if (!match) {
      return part;
    }

    const value = values[match[1]];
    return createElement(Fragment, { key: index }, value == null ? '' : String(value));
  });

  return createElement(Fragment, null, ...children);
}
