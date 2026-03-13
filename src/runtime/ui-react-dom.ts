import React from 'react';

/**
 * React DOM implementation of Sky UI components.
 */

export const text = (props: any) => (value: string) => 
  React.createElement('span', props, value);

export const column = (props: any) => (children: any[]) => 
  React.createElement('div', { 
    ...props, 
    style: { display: 'flex', flexDirection: 'column', ...(props.style || {}) } 
  }, ...children);

export const row = (props: any) => (children: any[]) => 
  React.createElement('div', { 
    ...props, 
    style: { display: 'flex', flexDirection: 'row', ...(props.style || {}) } 
  }, ...children);

export const button = (props: any) => (label: string) => 
  React.createElement('button', {
    ...props,
    onClick: props.onPress || props.onClick
  }, label);

export const image = (props: any) => 
  React.createElement('img', props);

export const scrollView = (props: any) => (children: any[]) => 
  React.createElement('div', { 
    ...props, 
    style: { overflow: 'scroll', ...(props.style || {}) } 
  }, ...children);

export const spacer = (props: any) => 
  React.createElement('div', { 
    ...props, 
    style: { flex: 1, ...(props.style || {}) } 
  });
